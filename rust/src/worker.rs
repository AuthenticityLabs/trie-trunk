use std::{
    ffi::OsString,
    io::{self, BufWriter, Read, Write},
    net::Shutdown,
    os::unix::net::{UnixListener, UnixStream},
    path::PathBuf,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
        mpsc,
    },
    thread,
    time::Duration,
};
use trie_trunk_search::{runtime, search};
const MAX: usize = 65536;
struct Output(UnixStream);
impl Write for Output {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        runtime::check()?;
        let n = bytes.len().min(MAX);
        self.0.write_all(&(n as u32).to_be_bytes())?;
        self.0.write_all(&bytes[..n])?;
        Ok(n)
    }
    fn flush(&mut self) -> io::Result<()> {
        self.0.flush()
    }
}
fn serve(mut stream: UnixStream, root: &PathBuf) -> io::Result<()> {
    stream.set_read_timeout(Some(Duration::from_secs(10)))?;
    let mut len = [0; 4];
    stream.read_exact(&mut len)?;
    let n = u32::from_be_bytes(len) as usize;
    if n > MAX {
        return Err(io::Error::other("worker request too large"));
    }
    let mut request = vec![0; n];
    stream.read_exact(&mut request)?;
    let args: Vec<String> = serde_json::from_slice(&request)?;
    let config =
        super::parse_args(args.into_iter().map(OsString::from)).map_err(io::Error::other)?;
    if &config.root != root {
        return Err(io::Error::other("worker root mismatch"));
    }
    stream.set_read_timeout(None)?;
    let cancel = Arc::new(AtomicBool::new(false));
    let signal = Arc::clone(&cancel);
    let mut reader = stream.try_clone()?;
    let monitor = thread::spawn(move || {
        let _ = reader.read(&mut [0]);
        signal.store(true, Ordering::Relaxed);
    });
    runtime::install(cancel);
    let mut output = BufWriter::with_capacity(MAX, Output(stream.try_clone()?));
    let result = search::run(&config, &mut output).and_then(|_| output.flush());
    runtime::clear();
    if let Err(error) = result {
        // Do not flush a partially encoded failing record.
        let message = error.to_string();
        let bytes = &message.as_bytes()[..message.len().min(MAX)];
        let _ = stream.write_all(&u32::MAX.to_be_bytes());
        let _ = stream.write_all(&(bytes.len() as u32).to_be_bytes());
        let _ = stream.write_all(bytes);
    } else {
        let _ = stream.write_all(&0u32.to_be_bytes());
    }
    // Discard the BufWriter on error without its drop attempting another flush.
    let _ = output.into_parts();
    let _ = stream.shutdown(Shutdown::Both);
    let _ = monitor.join();
    Ok(())
}
pub fn run(mut args: impl Iterator<Item = OsString>) -> io::Result<()> {
    let root = PathBuf::from(
        args.next()
            .ok_or_else(|| io::Error::other("worker root required"))?,
    );
    let path = args
        .next()
        .ok_or_else(|| io::Error::other("worker socket required"))?;
    let count = args
        .next()
        .and_then(|n| n.to_str()?.parse::<usize>().ok())
        .filter(|n| (1..=64).contains(n))
        .ok_or_else(|| io::Error::other("worker count must be 1..64"))?;
    let listener = UnixListener::bind(path)?;
    thread::spawn(|| {
        let _ = io::stdin().read(&mut [0]);
        std::process::exit(0);
    });
    let (tx, rx) = mpsc::sync_channel::<UnixStream>(count);
    let rx = Arc::new(Mutex::new(rx));
    for _ in 0..count {
        let rx = Arc::clone(&rx);
        let root = root.clone();
        thread::spawn(move || {
            loop {
                let stream = match rx.lock().unwrap().recv() {
                    Ok(s) => s,
                    Err(_) => break,
                };
                if let Err(error) = serve(stream, &root) {
                    eprintln!("worker: {error}");
                }
            }
        });
    }
    println!("READY");
    io::stdout().flush()?;
    for stream in listener.incoming() {
        tx.send(stream?).map_err(io::Error::other)?;
    }
    Ok(())
}

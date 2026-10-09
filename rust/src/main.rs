mod worker;
use std::env;
use std::io::{self, BufWriter, Write};
use std::path::PathBuf;
use std::process::ExitCode;
use trie_trunk_search::search::{self, Config};

fn parse_args(mut args: impl Iterator<Item = std::ffi::OsString>) -> Result<Config, String> {
    let (mut start, mut end) = (None, None);
    let mut config = Config {
        root: PathBuf::from("./logs"),
        tap: String::new(),
        start: 0,
        end: 0,
        headers: Vec::new(),
        pretty: false,
        compact: false,
        pil: false,
        pil_v2: false,
        pil_known: Vec::new(),
        limit: None,
        tail: false,
        follow: false,
        clauses: Vec::new(),
    };
    while let Some(flag) = args.next() {
        let flag = flag.to_str().ok_or("invalid option")?;
        match flag {
            "--pil-v2" => {
                config.pil = true;
                config.pil_v2 = true;
                continue;
            }
            "--pil" => {
                config.pil = true;
                continue;
            }
            "--compact" => {
                config.compact = true;
                continue;
            }
            "--pretty" => {
                config.pretty = true;
                continue;
            }
            "--follow" => {
                config.follow = true;
                continue;
            }
            "--tail" => {
                config.tail = true;
                continue;
            }
            _ => {}
        }
        if !matches!(
            flag,
            "--dir"
                | "--tap"
                | "--from-ms"
                | "--to-ms"
                | "--entry-headers"
                | "--limit"
                | "--where"
                | "--known-pil"
        ) {
            return Err(format!(
                "unknown option {flag}; expected --dir, --tap, --from-ms, --to-ms, --entry-headers, --limit, --where, --tail, --follow or --pretty, --compact, --pil, --known-pil"
            ));
        }
        let value = args
            .next()
            .ok_or_else(|| format!("missing value for {flag}"))?;
        if flag == "--dir" {
            config.root = value.into();
            continue;
        }
        let value = value
            .to_str()
            .ok_or_else(|| format!("invalid value for {flag}"))?;
        match flag {
            "--known-pil" => {
                config.pil_known = value
                    .split(',')
                    .filter(|s| !s.is_empty())
                    .map(str::to_owned)
                    .collect()
            }
            "--tap" => config.tap = value.into(),
            "--where" => config.clauses.push(value.into()),
            "--limit" => config.limit = Some(value.parse().map_err(|_| "invalid event count")?),
            "--from-ms" => start = Some(value.parse().map_err(|_| "invalid start milliseconds")?),
            "--to-ms" => end = Some(value.parse().map_err(|_| "invalid end milliseconds")?),
            "--entry-headers" => config.headers = value.split(',').map(str::to_owned).collect(),
            _ => unreachable!(),
        }
    }
    config.start = start.ok_or("--from-ms is required")?;
    config.end = end.ok_or("--to-ms is required")?;
    Ok(config)
}

fn main() -> ExitCode {
    let result = if env::args().nth(1).as_deref() == Some("--worker") {
        worker::run(env::args_os().skip(2))
    } else if env::args()
        .nth(1)
        .as_deref()
        .is_some_and(|s| matches!(s, "--pil-schemas" | "--decode-pil"))
    {
        trie_trunk_search::pil::command(env::args().skip(1).collect())
    } else {
        parse_args(env::args_os().skip(1))
            .map_err(io::Error::other)
            .and_then(|config| {
                let mut output = BufWriter::with_capacity(65536, io::stdout().lock());
                let result = search::run(&config, &mut output);
                let flushed = output.flush();
                result.and(flushed)
            })
    };
    match result {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("{error}");
            ExitCode::FAILURE
        }
    }
}

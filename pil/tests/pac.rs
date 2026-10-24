use pil_codec::{
    ABSENT, Definition, FrameIndex,
    pac::{PacIndex, SOURCE_EXTENSION},
};

#[test]
fn go_prefix_fixture_borrowed_roundtrip_and_malformed() {
    let raw = include_bytes!("fixtures/pac/packed.pac");
    let d = Definition::from_cbor(include_bytes!("fixtures/pac/definition.cbor")).unwrap();
    let digest: [u8; 32] = raw[32..64].try_into().unwrap();
    let mut pac = PacIndex::default();
    assert_eq!(
        pac.scan(&d, raw, &digest, &[SOURCE_EXTENSION], 64 << 20)
            .unwrap(),
        raw.len()
    );
    assert_eq!(pac.members().len(), 16);
    let [cs, ce] = pac.common();
    let common = &raw[cs as usize..ce as usize];
    let mut c = FrameIndex::default();
    c.scan(&d, common, 64 << 20).unwrap();
    assert_ne!(c.spans()[1][0], ABSENT);
    assert_eq!(c.spans()[0][0], ABSENT);
    let mut m = FrameIndex::default();
    for [start, end] in pac.members() {
        let member = &raw[*start as usize..*end as usize];
        m.scan(&d, member, 64 << 20).unwrap();
        assert_eq!(m.spans()[1][0], ABSENT);
        let (_, id) = pil_codec::pac::field(common, &c, member, &m, 1).unwrap();
        assert_eq!(id, [0,0,0,0,0,0,0x70,0,0x80,0,0,0,0,0,0,42]);
        let (_, event) = pil_codec::pac::field(common, &c, member, &m, 0).unwrap();
        assert_eq!(event.len(), 16);
    }
    for end in 0..raw.len() {
        assert!(
            pac.scan(&d, &raw[..end], &digest, &[SOURCE_EXTENSION], 64 << 20)
                .is_err()
        );
    }
    let mut bad = raw.to_vec();
    bad[32] ^= 1;
    assert!(
        pac.scan(&d, &bad, &digest, &[SOURCE_EXTENSION], 64 << 20)
            .is_err()
    );
    let mut bad = raw.to_vec();
    bad[4] = 2;
    assert!(
        pac.scan(&d, &bad, &digest, &[SOURCE_EXTENSION], 64 << 20)
            .is_err()
    );
}

use crate::{Cell, Result};
use tantivy::TantivyDocument;
use tantivy::schema::{FAST, INDEXED, STRING, Schema, TEXT};
pub fn schema(kinds: &[String]) -> Schema {
    let mut b = Schema::builder();
    b.add_u64_field("r", INDEXED);
    b.add_u64_field("rh", FAST);
    b.add_u64_field("rn", FAST);
    for (k, kind) in kinds.iter().enumerate() {
        b.add_u64_field(&format!("p{k}"), INDEXED);
        if matches!(kind.as_str(), "s" | "m" | "j") {
            b.add_text_field(&format!("c{k}"), STRING);
        }
        match kind.as_str() {
            "e" => {
                b.add_text_field(&format!("e{k}"), STRING);
            }
            "s" => {
                b.add_text_field(&format!("t{k}"), TEXT);
                b.add_text_field(&format!("e{k}"), STRING);
            }
            "m" => {
                b.add_text_field(&format!("e{k}"), STRING);
                b.add_text_field(&format!("t{k}"), TEXT);
                b.add_u64_field(&format!("u{k}"), INDEXED);
            }
            "u" => {
                b.add_u64_field(&format!("u{k}"), INDEXED | FAST);
            }
            "i" => {
                b.add_i64_field(&format!("i{k}"), INDEXED | FAST);
            }
            "f" => {
                b.add_f64_field(&format!("f{k}"), INDEXED | FAST);
            }
            "b" => {
                b.add_bool_field(&format!("b{k}"), INDEXED);
            }
            "j" => {
                b.add_text_field(&format!("t{k}"), TEXT);
            }
            _ => {}
        }
    }
    b.build()
}

pub fn document(schema: &Schema, row_value: u64, cells: &[Cell]) -> Result<TantivyDocument> {
    let mut doc = TantivyDocument::default();
    doc.add_u64(schema.get_field("r")?, row_value);
    doc.add_u64(schema.get_field("rh")?, row_value >> 32);
    doc.add_u64(schema.get_field("rn")?, row_value & 0xffff_ffff);
    for c in cells {
        doc.add_u64(schema.get_field(&format!("p{}", c.k))?, c.state);
        if let Ok(f) = schema.get_field(&format!("e{}", c.k)) {
            for v in &c.e {
                doc.add_text(f, v);
            }
        }
        if let Ok(f) = schema.get_field(&format!("c{}", c.k)) {
            for v in &c.c {
                doc.add_text(f, v);
            }
        }
        if let Ok(f) = schema.get_field(&format!("t{}", c.k)) {
            for v in &c.t {
                doc.add_text(f, v);
            }
        }
        if let Ok(f) = schema.get_field(&format!("e{}", c.k)) {
            for v in &c.t {
                doc.add_text(f, v);
            }
        }
        if let Ok(f) = schema.get_field(&format!("u{}", c.k)) {
            for &v in &c.u {
                doc.add_u64(f, v);
            }
        }
        if let Ok(f) = schema.get_field(&format!("i{}", c.k)) {
            for &v in &c.i {
                doc.add_i64(f, v);
            }
        }
        if let Ok(f) = schema.get_field(&format!("f{}", c.k)) {
            for &v in &c.f {
                doc.add_f64(f, v);
            }
        }
        if let Ok(f) = schema.get_field(&format!("b{}", c.k)) {
            for &v in &c.b {
                doc.add_bool(f, v);
            }
        }
    }

    Ok(doc)
}

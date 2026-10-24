//! Predicate planning and result enumeration stay in native memory.
use super::{Filter, Handle, Request, Result, native};
use std::ops::Bound;
use tantivy::Term;
use tantivy::collector::DocSetCollector;
use tantivy::query::{
    BooleanQuery, EmptyQuery, FuzzyTermQuery, Occur, PhrasePrefixQuery, PhraseQuery, Query,
    RangeQuery, RegexQuery, TermQuery,
};
use tantivy::schema::{Field, IndexRecordOption, Schema};
use tantivy::tokenizer::TokenStream;

impl Handle {
    pub(super) fn search(&self, r: &Request) -> Result<Vec<u64>> {
        self.search_cancel(r, None)
    }
    pub(super) fn search_cancel(
        &self,
        r: &Request,
        cancel: Option<std::sync::Arc<std::sync::atomic::AtomicBool>>,
    ) -> Result<Vec<u64>> {
        let schema = self.index.schema();
        let row = schema.get_field("r")?;
        let ranges: Vec<Box<dyn Query>> = r
            .ranges
            .iter()
            .filter(|(a, b)| a < b)
            .map(|&(a, b)| {
                Box::new(RangeQuery::new(
                    Bound::Included(Term::from_field_u64(row, a)),
                    Bound::Excluded(Term::from_field_u64(row, b)),
                )) as Box<dyn Query>
            })
            .collect();
        let mut clauses: Vec<(Occur, Box<dyn Query>)> = vec![(Occur::Must, union(ranges))];
        for filter in &r.filters {
            clauses.push((Occur::Must, predicate(&schema, filter)?));
        }
        if !r.text.is_empty() {
            clauses.push((Occur::Must, self.text_query(r)?));
        }
        let searcher = self.reader.searcher();
        let query = BooleanQuery::new(clauses);
        let hits = if let Some(cancel) = cancel {
            searcher.search(&query, &native::CancelCollector(cancel))?
        } else {
            searcher.search(&query, &DocSetCollector)?
        };
        let columns = searcher
            .segment_readers()
            .iter()
            .map(|s| Ok((s.fast_fields().u64("rh")?, s.fast_fields().u64("rn")?)))
            .collect::<tantivy::Result<Vec<_>>>()?;
        let mut rows: Vec<u64> = hits
            .into_iter()
            .filter_map(|d| {
                let (h, n) = &columns[d.segment_ord as usize];
                Some(h.first(d.doc_id)? << 32 | n.first(d.doc_id)?)
            })
            .collect();
        rows.sort_unstable();
        rows.dedup();
        Ok(rows)
    }
    pub(super) fn text_query(&self, r: &Request) -> Result<Box<dyn Query>> {
        if r.mode == "contains" {
            let queries = self
                .index
                .schema()
                .fields()
                .filter(|(_, e)| e.name().starts_with('c'))
                .map(|(f, _)| substring(f, &r.text))
                .collect::<Result<Vec<_>>>()?;
            return Ok(union(queries));
        }
        let mut analyzer = self
            .index
            .tokenizers()
            .get("default")
            .ok_or("missing tokenizer")?;
        let mut stream = analyzer.token_stream(&r.text);
        let mut words = Vec::new();
        while stream.advance() {
            words.push(stream.token().text.clone());
        }
        if words.is_empty() {
            return Ok(Box::new(EmptyQuery));
        }
        if words.len() > 64 {
            return Err("search supports at most 64 terms".into());
        }
        let fields: Vec<Field> = self
            .index
            .schema()
            .fields()
            .filter(|(_, e)| e.name().starts_with('t'))
            .map(|(f, _)| f)
            .collect();
        let term_query = |f: Field, w: &str| -> Box<dyn Query> {
            let term = Term::from_field_text(f, w);
            if r.mode == "fuzzy" {
                Box::new(FuzzyTermQuery::new(term, 1, true))
            } else {
                Box::new(TermQuery::new(term, IndexRecordOption::Basic))
            }
        };
        match r.mode.as_str() {
            "terms" | "fuzzy" => Ok(Box::new(BooleanQuery::new(
                words
                    .iter()
                    .map(|w| {
                        (
                            Occur::Must,
                            union(fields.iter().map(|&f| term_query(f, w)).collect()),
                        )
                    })
                    .collect(),
            ))),
            "phrase" | "prefix" => Ok(union(
                fields
                    .iter()
                    .map(|&f| {
                        let terms: Vec<_> =
                            words.iter().map(|w| Term::from_field_text(f, w)).collect();
                        if r.mode == "prefix" {
                            let mut query = PhrasePrefixQuery::new(terms);
                            query.set_max_expansions(u32::MAX);
                            Box::new(query) as Box<dyn Query>
                        } else if terms.len() == 1 {
                            term_query(f, &words[0])
                        } else {
                            Box::new(PhraseQuery::new(terms))
                        }
                    })
                    .collect(),
            )),
            _ => Err("unsupported indexed text mode".into()),
        }
    }
}
fn union(queries: Vec<Box<dyn Query>>) -> Box<dyn Query> {
    if queries.is_empty() {
        Box::new(EmptyQuery)
    } else {
        Box::new(BooleanQuery::new(
            queries.into_iter().map(|q| (Occur::Should, q)).collect(),
        ))
    }
}
fn substring(field: Field, text: &str) -> Result<Box<dyn Query>> {
    let mut escaped = String::new();
    for c in text.chars() {
        if "\\.*+?()[]{}|^$".contains(c) {
            escaped.push('\\');
        }
        escaped.push(c);
    }
    Ok(Box::new(RegexQuery::from_pattern(
        &format!(".*{}.*", escaped),
        field,
    )?))
}
pub(super) fn predicate(s: &Schema, f: &Filter) -> Result<Box<dyn Query>> {
    if f.kind == "finite" {
        let field = s.get_field(&format!("f{}", f.k))?;
        return Ok(Box::new(RangeQuery::new(
            Bound::Included(Term::from_field_f64(field, -f64::MAX)),
            Bound::Included(Term::from_field_f64(field, f64::MAX)),
        )));
    }
    if f.kind == "never" {
        return Ok(Box::new(EmptyQuery));
    }
    if f.op == "!=" {
        let equal = Filter {
            k: f.k,
            kind: f.kind.clone(),
            op: "==".into(),
            value: f.value.clone(),
            code: f.code,
        };
        let present: Box<dyn Query> = if f.kind == "f" {
            predicate(
                s,
                &Filter {
                    k: f.k,
                    kind: "finite".into(),
                    ..Default::default()
                },
            )?
        } else {
            Box::new(TermQuery::new(
                Term::from_field_u64(s.get_field(&format!("p{}", f.k))?, 2),
                IndexRecordOption::Basic,
            ))
        };
        return Ok(Box::new(BooleanQuery::new(vec![
            (Occur::Must, present),
            (Occur::MustNot, predicate(s, &equal)?),
        ])));
    }
    if f.kind == "c" {
        return substring(s.get_field(&format!("c{}", f.k))?, &f.value);
    }
    if f.kind == "m" {
        let exact = s.get_field(&format!("e{}", f.k))?;
        let mut terms: Vec<Box<dyn Query>> = vec![Box::new(TermQuery::new(
            Term::from_field_text(exact, &f.value),
            IndexRecordOption::Basic,
        ))];
        if let Some(code) = f.code {
            terms.push(Box::new(TermQuery::new(
                Term::from_field_u64(s.get_field(&format!("u{}", f.k))?, code),
                IndexRecordOption::Basic,
            )));
        }
        return Ok(union(terms));
    }
    let field = s.get_field(&format!("{}{}", f.kind, f.k))?;
    let term = match f.kind.as_str() {
        "u" | "p" => Term::from_field_u64(field, f.value.parse()?),
        "i" => Term::from_field_i64(field, f.value.parse()?),
        "f" => Term::from_field_f64(field, f.value.parse()?),
        "b" => Term::from_field_bool(field, f.value.parse()?),
        "e" => Term::from_field_text(field, &f.value),
        _ => return Err("unsupported predicate kind".into()),
    };
    Ok(match f.op.as_str() {
        "==" => Box::new(TermQuery::new(term, IndexRecordOption::Basic)),
        ">" => Box::new(RangeQuery::new(Bound::Excluded(term), Bound::Unbounded)),
        ">=" => Box::new(RangeQuery::new(Bound::Included(term), Bound::Unbounded)),
        "<" => Box::new(RangeQuery::new(Bound::Unbounded, Bound::Excluded(term))),
        "<=" => Box::new(RangeQuery::new(Bound::Unbounded, Bound::Included(term))),
        _ => return Err("unsupported indexed predicate".into()),
    })
}

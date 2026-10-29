use ciborium::Value;
use serde_json::Value as Json;
use std::collections::BTreeMap;
use std::io;

use crate::schema::Schema;
use crate::store::invalid;

const UNKNOWN: u8 = 0;
const FALSE: u8 = 1;
const TRUE: u8 = 2;

#[derive(Clone, PartialEq)]
enum Operator {
    Equal(Json),
    Different(Json),
    Null,
    NotNull,
}

#[derive(Clone, PartialEq)]
struct Predicate {
    name: String,
    operator: Operator,
}

#[derive(Clone, Default)]
pub(crate) struct Filter {
    predicates: Vec<Predicate>,
    groups: Vec<Vec<usize>>,
}

impl Filter {
    pub fn is_empty(&self) -> bool {
        self.groups.is_empty()
    }

    pub fn parse(clauses: &[String]) -> io::Result<Self> {
        let mut filter = Self::default();
        for clause in clauses {
            let mut input = clause.trim();
            let mut group = Vec::new();
            while !input.is_empty() {
                let (predicate, rest) = parse_predicate(input)?;
                let index = filter.predicates.iter().position(|p| p == &predicate);
                let index = index.unwrap_or_else(|| {
                    filter.predicates.push(predicate);
                    filter.predicates.len() - 1
                });
                if !group.contains(&index) {
                    group.push(index);
                }
                if !rest.is_empty() && !rest.starts_with(char::is_whitespace) {
                    return Err(invalid("separate predicates with whitespace"));
                }
                input = rest.trim_start();
            }
            if group.is_empty() {
                return Err(invalid("--where requires at least one predicate"));
            }
            filter.groups.push(group);
        }
        Ok(filter)
    }

    pub fn bind(&self, schema: &Schema) -> io::Result<Option<BoundFilter>> {
        if self.groups.is_empty() {
            return Ok(None);
        }
        let mut bound = BoundFilter {
            predicates: Vec::new(),
            groups: self.groups.clone(),
            by_key: BTreeMap::new(),
            stride: self.predicates.len().div_ceil(4),
            states: vec![UNKNOWN; self.predicates.len().div_ceil(4)],
            decisions: Vec::new(),
        };
        for (index, predicate) in self.predicates.iter().enumerate() {
            let key = schema.key(&predicate.name)?;
            bound.predicates.push(BoundPredicate {
                key,
                operator: predicate.operator.clone(),
            });
            if let Some(key) = key {
                bound.by_key.entry(key).or_default().push(index);
            } else {
                let matches = matches!(predicate.operator, Operator::Null);
                set(&mut bound.states, index, truth(matches));
            }
        }
        bound.decisions.push(bound.classify(0));
        Ok(Some(bound))
    }
}

fn parse_predicate(input: &str) -> io::Result<(Predicate, &str)> {
    let end = input
        .find(|c: char| !c.is_ascii_alphanumeric() && c != '_' && c != '-')
        .unwrap_or(input.len());
    let name = &input[..end];
    if name.is_empty()
        || !name.starts_with(|c: char| c.is_ascii_alphabetic() || c == '_')
        || name.eq_ignore_ascii_case("AND")
        || name.eq_ignore_ascii_case("OR")
    {
        return Err(invalid(
            "expected a top-level field; use whitespace within --where and repeat --where for alternatives",
        ));
    }
    let rest = input[end..].trim_start();
    let (operator, rest) = if let Some(rest) = rest.strip_prefix("is")
        && rest.starts_with(char::is_whitespace)
    {
        let rest = rest.trim_start();
        if let Some(rest) = rest.strip_prefix("!null") {
            (Operator::NotNull, rest)
        } else if let Some(rest) = rest.strip_prefix("null") {
            (Operator::Null, rest)
        } else {
            return Err(invalid("expected is null or is !null"));
        }
    } else {
        let (different, rest) = if let Some(rest) = rest.strip_prefix("==") {
            (false, rest)
        } else if let Some(rest) = rest.strip_prefix("!=") {
            (true, rest)
        } else {
            return Err(invalid(
                "expected ==, !=, is null or is !null after a top-level field",
            ));
        };
        let rest = rest.trim_start();
        let mut values = serde_json::Deserializer::from_str(rest).into_iter::<Json>();
        let value = values
            .next()
            .ok_or_else(|| invalid("missing comparison value"))?
            .map_err(|error| {
                invalid(format!(
                    "expected a JSON string, number or boolean: {error}"
                ))
            })?;
        if !matches!(value, Json::String(_) | Json::Number(_) | Json::Bool(_)) {
            return Err(invalid(
                "compare scalar values; use is null or is !null for null checks",
            ));
        }
        let rest = &rest[values.byte_offset()..];
        (
            if different {
                Operator::Different(value)
            } else {
                Operator::Equal(value)
            },
            rest,
        )
    };
    Ok((
        Predicate {
            name: name.into(),
            operator,
        },
        rest,
    ))
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(crate) enum Decision {
    Include,
    Exclude,
    Payload,
}

struct BoundPredicate {
    key: Option<u16>,
    operator: Operator,
}

impl BoundPredicate {
    fn matches(&self, schema: &Schema, value: Option<&Value>) -> bool {
        let value = value.filter(|value| !matches!(value, Value::Null));
        match &self.operator {
            Operator::Null => value.is_none(),
            Operator::NotNull => value.is_some(),
            Operator::Equal(expected) => {
                value.is_some_and(|value| schema.equals(self.key.unwrap(), value, expected))
            }
            Operator::Different(expected) => {
                value.is_some_and(|value| !schema.equals(self.key.unwrap(), value, expected))
            }
        }
    }
}

pub(crate) struct BoundFilter {
    predicates: Vec<BoundPredicate>,
    groups: Vec<Vec<usize>>,
    by_key: BTreeMap<u16, Vec<usize>>,
    stride: usize,
    states: Vec<u8>,
    decisions: Vec<Decision>,
}

impl BoundFilter {
    pub fn push(&mut self, parent: u32, key: u16, value: u64, schema: &Schema) {
        let parent = parent as usize * self.stride;
        let start = self.states.len();
        self.states.extend_from_within(parent..parent + self.stride);
        if let Some(predicates) = self.by_key.get(&key) {
            let value = Value::Integer(value.into());
            for &index in predicates {
                if get(&self.states[start..], index) == UNKNOWN {
                    let matches = self.predicates[index].matches(schema, Some(&value));
                    set(&mut self.states[start..], index, truth(matches));
                }
            }
        }
        self.decisions.push(self.classify(start));
    }

    fn classify(&self, start: usize) -> Decision {
        let states = &self.states[start..];
        let mut unresolved = false;
        for group in &self.groups {
            let mut complete = true;
            let mut rejected = false;
            for &index in group {
                match get(states, index) {
                    FALSE => {
                        rejected = true;
                        break;
                    }
                    UNKNOWN => complete = false,
                    _ => {}
                }
            }
            if !rejected {
                if complete {
                    return Decision::Include;
                }
                unresolved = true;
            }
        }
        if unresolved {
            Decision::Payload
        } else {
            Decision::Exclude
        }
    }

    pub fn decision(&self, leaf: u32) -> io::Result<Decision> {
        self.decisions
            .get(leaf as usize)
            .copied()
            .ok_or_else(|| invalid("invalid trie leaf"))
    }

    pub fn matches(&self, leaf: u32, schema: &Schema, payload: &BTreeMap<u16, Value>) -> bool {
        let states = &self.states[leaf as usize * self.stride..];
        self.groups.iter().any(|group| {
            group.iter().all(|&index| match get(states, index) {
                FALSE => false,
                TRUE => true,
                _ => {
                    let predicate = &self.predicates[index];
                    predicate.matches(schema, predicate.key.and_then(|key| payload.get(&key)))
                }
            })
        })
    }
}

fn truth(matches: bool) -> u8 {
    if matches { TRUE } else { FALSE }
}

fn get(states: &[u8], index: usize) -> u8 {
    (states[index / 4] >> ((index % 4) * 2)) & 3
}

fn set(states: &mut [u8], index: usize, value: u8) {
    let shift = (index % 4) * 2;
    states[index / 4] = (states[index / 4] & !(3 << shift)) | (value << shift);
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn schema() -> Schema {
        let value = Value::Map(vec![
            (0.into(), 1.into()),
            (4.into(), "UTC".into()),
            (
                1.into(),
                Value::Map(vec![
                    (
                        8.into(),
                        Value::Map(vec![(0.into(), "id".into()), (6.into(), "uuidv7".into())]),
                    ),
                    (
                        12.into(),
                        Value::Map(vec![(0.into(), "duration".into()), (5.into(), "us".into())]),
                    ),
                    (
                        3.into(),
                        Value::Map(vec![
                            (0.into(), "service".into()),
                            (
                                2.into(),
                                Value::Map(vec![
                                    (1.into(), "vault".into()),
                                    (2.into(), "billing".into()),
                                ]),
                            ),
                        ]),
                    ),
                    (
                        14.into(),
                        Value::Map(vec![(0.into(), "http_status".into())]),
                    ),
                    (16.into(), Value::Map(vec![(0.into(), "error".into())])),
                ]),
            ),
        ]);
        let bytes = pil_codec::object::encode_value(&value).unwrap();
        Schema::parse(&bytes, 1).unwrap()
    }

    #[test]
    fn parses_complete_predicates_and_deduplicates() {
        let filter = Filter::parse(&[
            r#"service == "vault" http_status!=403 error is !null"#.into(),
            r#"service=="vault" error is null"#.into(),
            "message==\"quoted \\\"value\\\" 雪\\nline\" active==false score==-1.25e2".into(),
        ])
        .unwrap();
        assert_eq!(filter.groups.len(), 3);
        assert_eq!(filter.predicates.len(), 7);
        assert_eq!(filter.groups[0][0], filter.groups[1][0]);
    }

    #[test]
    fn rejects_unsupported_or_ambiguous_syntax() {
        for clause in [
            "",
            " ",
            "service=vault",
            "service==vault",
            "x==null",
            "x!=null",
            "x==[]",
            "x=={}",
            "x is full",
            "x is not null",
            "x is nulljunk",
            "x==1 AND y==2",
            "x==1 OR y==2",
            "(x==1)",
            "x==1 y",
            "x==\"bad",
            "x==1y==2",
            "x==truey==2",
            "error.code==1",
            "x>1",
            "x==1, y==2",
        ] {
            assert!(Filter::parse(&[clause.into()]).is_err(), "{clause}");
        }
    }

    #[test]
    fn lineage_decisions_match_full_record_evaluation() {
        let schema = schema();
        let nodes = [(0, 3, 1), (1, 14, 401), (0, 3, 2), (3, 14, 500)];
        for clauses in [
            vec![
                r#"service=="vault" http_status==401"#,
                r#"service=="billing" error is !null"#,
            ],
            vec![r#"service!="vault""#],
            vec!["error is null"],
            vec!["error is !null"],
            vec!["missing is null http_status!=500"],
            vec!["missing!=1"],
            vec!["missing==1", "service is !null"],
            vec!["http_status==401 http_status!=401"],
            vec![r#"service=="vault" service=="billing""#],
        ] {
            let filter =
                Filter::parse(&clauses.iter().map(|s| s.to_string()).collect::<Vec<_>>()).unwrap();
            let mut bound = filter.bind(&schema).unwrap().unwrap();
            for &(parent, key, value) in &nodes {
                bound.push(parent, key, value, &schema);
            }
            for leaf in 0..=nodes.len() {
                for error in [
                    None,
                    Some(Value::Null),
                    Some(Value::Bool(false)),
                    Some(Value::Text(String::new())),
                ] {
                    for service in [
                        None,
                        Some(Value::Text("vault".into())),
                        Some(Value::Text("billing".into())),
                    ] {
                        let mut payload = BTreeMap::new();
                        if let Some(value) = error.clone() {
                            payload.insert(16, value);
                        }
                        if let Some(value) = service.clone() {
                            payload.insert(3, value);
                        }
                        let mut record = payload.clone();
                        let mut parent = leaf;
                        while parent != 0 {
                            let (next, key, value) = nodes[parent - 1];
                            record.insert(key, Value::Integer(value.into()));
                            parent = next as usize;
                        }
                        let expected = bound.groups.iter().any(|group| {
                            group.iter().all(|&index| {
                                let predicate = &bound.predicates[index];
                                predicate.matches(
                                    &schema,
                                    predicate.key.and_then(|key| record.get(&key)),
                                )
                            })
                        });
                        let actual = match bound.decision(leaf as u32).unwrap() {
                            Decision::Include => true,
                            Decision::Exclude => false,
                            Decision::Payload => {
                                bound.matches(leaf as u32, &schema, &payload)
                            }
                        };
                        assert_eq!(actual, expected, "{clauses:?} leaf {leaf} {record:?}");
                    }
                }
            }
        }
    }

    #[test]
    fn packed_states_cross_byte_boundaries_and_extend() {
        let schema = schema();
        let clause = (0..130)
            .map(|n| format!("http_status!={n}"))
            .collect::<Vec<_>>()
            .join(" ");
        let mut filter = Filter::parse(&[clause])
            .unwrap()
            .bind(&schema)
            .unwrap()
            .unwrap();
        filter.push(0, 14, 401, &schema);
        assert_eq!(filter.decision(1).unwrap(), Decision::Include);
        filter.push(0, 14, 129, &schema);
        assert_eq!(filter.decision(2).unwrap(), Decision::Exclude);
        filter.push(1, 3, 1, &schema);
        assert_eq!(filter.decision(3).unwrap(), Decision::Include);
        assert!(filter.decision(4).is_err());
    }

    #[test]
    fn comparisons_preserve_types_and_integer_precision() {
        let schema = schema();
        assert!(schema.equals(3, &1.into(), &json!("vault")));
        assert!(!schema.equals(3, &1.into(), &json!(1)));
        assert!(schema.equals(3, &Value::Text("vault".into()), &json!("vault")));
        assert!(!schema.equals(14, &401.into(), &json!("401")));
        assert!(schema.equals(14, &401.into(), &json!(401.0)));
        assert!(schema.equals(14, &Value::Integer(u64::MAX.into()), &json!(u64::MAX)));
        assert!(!schema.equals(
            14,
            &Value::Integer(9_007_199_254_740_993u64.into()),
            &json!(9_007_199_254_740_992.0)
        ));
        assert!(schema.equals(14, &Value::Float(1.0), &json!(1)));
        assert!(!schema.equals(14, &Value::Float(1.25), &json!(1)));
        assert!(!schema.equals(14, &Value::Bool(false), &json!(0)));
    }
}

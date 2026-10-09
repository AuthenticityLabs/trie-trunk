use std::fmt;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Error {
    Incomplete,
    Invalid(&'static str),
    Definition(String),
    Overflow,
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Incomplete => f.write_str("incomplete PIL data"),
            Self::Invalid(message) => write!(f, "invalid PIL data: {message}"),
            Self::Definition(message) => write!(f, "invalid PIL Definition: {message}"),
            Self::Overflow => f.write_str("PIL size exceeds the addressable range"),
        }
    }
}

impl std::error::Error for Error {}

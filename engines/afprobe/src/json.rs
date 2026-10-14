//! Minimal JSON value and pretty printer.
//!
//! afprobe only *writes* JSON (its report), so a small value type is enough
//! and keeps the dependency set to `libc`. Output is RFC 8259 JSON; non-finite
//! numbers are written as `null`.

use std::fmt::Write as _;

/// A JSON value. Objects keep insertion order so reports are stable and easy
/// to diff.
#[derive(Debug, Clone, PartialEq)]
pub enum Json {
    Null,
    Bool(bool),
    Int(i64),
    UInt(u64),
    Num(f64),
    Str(String),
    Arr(Vec<Json>),
    Obj(Vec<(String, Json)>),
}

impl Json {
    /// An empty object.
    pub fn obj() -> Self {
        Json::Obj(Vec::new())
    }

    /// Builder form of [`Json::insert`].
    pub fn with(mut self, key: &str, value: impl Into<Json>) -> Self {
        self.insert(key, value);
        self
    }

    /// Appends `key: value` to an object (no-op on other variants).
    pub fn insert(&mut self, key: &str, value: impl Into<Json>) {
        if let Json::Obj(m) = self {
            m.push((key.to_string(), value.into()));
        }
    }

    /// Looks up a key in an object.
    pub fn get(&self, key: &str) -> Option<&Json> {
        match self {
            Json::Obj(m) => m.iter().find(|(k, _)| k == key).map(|(_, v)| v),
            _ => None,
        }
    }

    /// Two-space indented rendering with a trailing newline.
    pub fn pretty(&self) -> String {
        let mut s = String::new();
        self.write(&mut s, 0);
        s.push('\n');
        s
    }

    fn write(&self, out: &mut String, level: usize) {
        match self {
            Json::Null => out.push_str("null"),
            Json::Bool(b) => out.push_str(if *b { "true" } else { "false" }),
            Json::Int(i) => {
                let _ = write!(out, "{i}");
            }
            Json::UInt(u) => {
                let _ = write!(out, "{u}");
            }
            Json::Num(f) => write_f64(out, *f),
            Json::Str(s) => escape(out, s),
            Json::Arr(a) => {
                if a.is_empty() {
                    out.push_str("[]");
                    return;
                }
                out.push_str("[\n");
                for (i, v) in a.iter().enumerate() {
                    indent(out, level + 1);
                    v.write(out, level + 1);
                    if i + 1 < a.len() {
                        out.push(',');
                    }
                    out.push('\n');
                }
                indent(out, level);
                out.push(']');
            }
            Json::Obj(m) => {
                if m.is_empty() {
                    out.push_str("{}");
                    return;
                }
                out.push_str("{\n");
                for (i, (k, v)) in m.iter().enumerate() {
                    indent(out, level + 1);
                    escape(out, k);
                    out.push_str(": ");
                    v.write(out, level + 1);
                    if i + 1 < m.len() {
                        out.push(',');
                    }
                    out.push('\n');
                }
                indent(out, level);
                out.push('}');
            }
        }
    }
}

fn indent(out: &mut String, level: usize) {
    for _ in 0..level {
        out.push_str("  ");
    }
}

fn write_f64(out: &mut String, f: f64) {
    if !f.is_finite() {
        out.push_str("null");
    } else if f.fract() == 0.0 && f.abs() < 1e15 {
        let _ = write!(out, "{}", f as i64);
    } else {
        // Rust's Display for f64 is the shortest round-trip form and never
        // uses exponent notation, so it is always valid JSON.
        let _ = write!(out, "{f}");
    }
}

fn escape(out: &mut String, s: &str) {
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => {
                let _ = write!(out, "\\u{:04x}", c as u32);
            }
            c => out.push(c),
        }
    }
    out.push('"');
}

impl From<bool> for Json {
    fn from(v: bool) -> Self {
        Json::Bool(v)
    }
}
impl From<i64> for Json {
    fn from(v: i64) -> Self {
        Json::Int(v)
    }
}
impl From<i32> for Json {
    fn from(v: i32) -> Self {
        Json::Int(v as i64)
    }
}
impl From<u64> for Json {
    fn from(v: u64) -> Self {
        Json::UInt(v)
    }
}
impl From<u32> for Json {
    fn from(v: u32) -> Self {
        Json::UInt(v as u64)
    }
}
impl From<usize> for Json {
    fn from(v: usize) -> Self {
        Json::UInt(v as u64)
    }
}
impl From<f64> for Json {
    fn from(v: f64) -> Self {
        Json::Num(v)
    }
}
impl From<&str> for Json {
    fn from(v: &str) -> Self {
        Json::Str(v.to_string())
    }
}
impl From<String> for Json {
    fn from(v: String) -> Self {
        Json::Str(v)
    }
}
impl From<Vec<Json>> for Json {
    fn from(v: Vec<Json>) -> Self {
        Json::Arr(v)
    }
}
impl<T: Into<Json>> From<Option<T>> for Json {
    fn from(v: Option<T>) -> Self {
        v.map_or(Json::Null, Into::into)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn renders_nested_values() {
        let j = Json::obj()
            .with("a", 1u64)
            .with("b", vec![Json::from("x"), Json::Null])
            .with("c", Json::obj())
            .with("d", 0.25)
            .with("e", f64::NAN)
            .with("f", Option::<u64>::None)
            .with("g", -3i64)
            .with("h", 1e20);
        assert_eq!(
            j.pretty(),
            "{\n  \"a\": 1,\n  \"b\": [\n    \"x\",\n    null\n  ],\n  \"c\": {},\n  \"d\": 0.25,\n  \"e\": null,\n  \"f\": null,\n  \"g\": -3,\n  \"h\": 100000000000000000000\n}\n"
        );
    }

    #[test]
    fn escapes_strings() {
        let mut s = String::new();
        escape(&mut s, "q\"b\\n\n\u{1}é");
        assert_eq!(s, "\"q\\\"b\\\\n\\n\\u0001é\"");
    }

    #[test]
    fn get_finds_keys() {
        let j = Json::obj().with("k", true);
        assert_eq!(j.get("k"), Some(&Json::Bool(true)));
        assert_eq!(j.get("x"), None);
    }
}

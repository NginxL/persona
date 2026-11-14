//! A chat item as the hub sends it (`docs/protocol/protocol.md`, "Item continuity").
//!
//! The core reads only the fields its rules use and keeps the item's JSON exactly as received, so the clients render
//! what the hub sent and nothing is lost in a round trip through the core.

use serde::Deserialize;
use serde_json::value::RawValue;

/// The closed range of work a fold item stands for, from the fold's own position through `through_seq` /
/// `through_id`.
#[derive(Debug, Clone, PartialEq)]
pub struct FoldRange {
    pub through_seq: i64,
    pub through_id: String,
    /// The fold's whole object, compared when the same fold id arrives again.
    value: serde_json::Value,
}

#[derive(Debug, Clone)]
pub struct Item {
    pub id: String,
    pub seq: i64,
    pub kind: String,
    pub role: String,
    pub tool_use_id: Option<String>,
    pub fold: Option<FoldRange>,
    pub supervision_user: bool,
    fold_supervision_user: bool,
    /// The item's JSON as the hub sent it.
    raw: Box<RawValue>,
}

#[derive(Deserialize)]
struct Head<'a> {
    #[serde(borrow)]
    id: std::borrow::Cow<'a, str>,
    #[serde(default)]
    seq: i64,
    #[serde(default, borrow)]
    kind: Option<std::borrow::Cow<'a, str>>,
    #[serde(default, borrow)]
    role: Option<std::borrow::Cow<'a, str>>,
    #[serde(default, borrow)]
    tool_use_id: Option<std::borrow::Cow<'a, str>>,
    #[serde(default)]
    fold: Option<serde_json::Value>,
    #[serde(default)]
    supervision_user: bool,
}

#[derive(Debug)]
pub struct ItemError(String);

impl std::fmt::Display for ItemError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for ItemError {}

impl Item {
    /// Reads one item from its JSON object.
    pub fn from_raw(raw: Box<RawValue>) -> Result<Item, ItemError> {
        let head: Head<'_> = serde_json::from_str(raw.get()).map_err(|e| ItemError(format!("item: {e}")))?;
        let fold_supervision_user = head
            .fold
            .as_ref()
            .is_some_and(|value| value.get("supervision_user").and_then(serde_json::Value::as_bool) == Some(true));
        let fold = match head.fold {
            Some(serde_json::Value::Null) | None => None,
            Some(value) => Some(FoldRange {
                through_seq: value
                    .get("through_seq")
                    .and_then(serde_json::Value::as_i64)
                    .unwrap_or(0),
                through_id: value
                    .get("through_id")
                    .and_then(serde_json::Value::as_str)
                    .unwrap_or("")
                    .to_owned(),
                value,
            }),
        };
        Ok(Item {
            id: head.id.into_owned(),
            seq: head.seq,
            kind: head.kind.map_or_else(|| "unknown".to_owned(), |k| k.into_owned()),
            role: head.role.map(|r| r.into_owned()).unwrap_or_default(),
            tool_use_id: head.tool_use_id.map(|t| t.into_owned()),
            fold,
            supervision_user: head.supervision_user,
            fold_supervision_user,
            raw,
        })
    }

    /// Reads a JSON array of items.
    pub fn list_from_json(json: &str) -> Result<Vec<Item>, ItemError> {
        let raws: Vec<Box<RawValue>> = serde_json::from_str(json).map_err(|e| ItemError(format!("items: {e}")))?;
        raws.into_iter().map(Item::from_raw).collect()
    }

    /// The item's JSON exactly as the hub sent it.
    pub fn json(&self) -> &str {
        self.raw.get()
    }

    /// Authoritative visibility evidence survives a later copy that omits it, as iOS's merge requires.
    pub fn has_supervision_evidence(&self) -> bool {
        self.supervision_user || self.fold_supervision_user
    }

    pub fn with_supervision(mut self) -> Self {
        self.supervision_user = true;
        self.fold_supervision_user = true;
        if let Ok(mut value) = serde_json::from_str::<serde_json::Value>(self.raw.get()) {
            value["supervision_user"] = serde_json::Value::Bool(true);
            if let Some(fold) = value.get_mut("fold").filter(|fold| fold.is_object()) {
                fold["supervision_user"] = serde_json::Value::Bool(true);
            }
            if let Ok(raw) = serde_json::value::to_raw_value(&value) {
                self.raw = raw;
            }
        }
        self
    }

    /// The number after the last `#` of the id: the block within one transcript line. Ids without one count as 0.
    pub fn part(&self) -> i64 {
        part(&self.id)
    }

    /// Work a fold can stand for: thinking, tools, folds, the assistant's narration and a tool's images. The
    /// assistant's text outside a fold's range is the answer and stays.
    pub fn is_work(&self) -> bool {
        match self.kind.as_str() {
            "thinking" | "tool_use" | "tool_result" | "fold" => true,
            "text" => self.role == "assistant",
            "image" => self.tool_use_id.is_some(),
            _ => false,
        }
    }

    /// Whether this fold covers `other`: from the fold's position through its `through_seq` / `through_id`, both ends
    /// included, comparing the seq and then the block number.
    pub fn folds(&self, other: &Item) -> bool {
        let Some(range) = &self.fold else { return false };
        let start_part = part(&self.id);
        let other_part = other.part();
        (other.seq > self.seq || other.seq == self.seq && other_part >= start_part)
            && (other.seq < range.through_seq
                || other.seq == range.through_seq && other_part <= part(&range.through_id))
    }

    /// The same fold arrived again unchanged.
    pub(crate) fn same_fold(&self, other: &Item) -> bool {
        match (&self.fold, &other.fold) {
            (Some(a), Some(b)) => a.value == b.value,
            (None, None) => true,
            _ => false,
        }
    }

    /// Ordering key of a chat's items.
    pub(crate) fn key(&self) -> (i64, i64) {
        (self.seq, self.part())
    }
}

pub(crate) fn part(id: &str) -> i64 {
    id.rsplit_once('#').and_then(|(_, n)| n.parse().ok()).unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parts_and_defaults() {
        assert_eq!(part("12#3"), 3);
        assert_eq!(part("fold:2#0"), 0);
        assert_eq!(part("plain"), 0);
        let item = Item::list_from_json(r#"[{"id":"a"}]"#).unwrap().remove(0);
        assert_eq!((item.seq, item.kind.as_str(), item.role.as_str()), (0, "unknown", ""));
        assert_eq!(item.json(), r#"{"id":"a"}"#);
    }
}

// Notification categories (push.md, api.yaml DevicePrefs.mute_categories): the hub sends no push
// of a muted category to a device. Pure (no imports) so node can test it.

export const CATEGORIES = [
  { id: 'sessions', label: 'Sessions', sub: 'Needs permission, asked a question, failed, summaries' },
  { id: 'limits', label: 'Limits', sub: 'Limit at 80% / 95%, window reset' },
  { id: 'switch', label: 'Account switch', sub: 'A session moved, the switch digest' },
  { id: 'drain', label: 'Drain', sub: 'Drain started, ended, or pulled early' },
  { id: 'usage', label: 'Usage notices', sub: 'Limits changed, usage reset, changed account, weekly drop' },
  { id: 'machines', label: 'Machines', sub: 'A machine went silent with live sessions' },
];

// prefs.mute_categories (missing, not a list, or unknown names) -> the muted ids we know.
export const mutedCategories = (prefs) => {
  const m = prefs && Array.isArray(prefs.mute_categories) ? prefs.mute_categories : [];
  return CATEGORIES.map((c) => c.id).filter((id) => m.includes(id));
};

// The new mute_categories after turning one category on or off. Names the board does not know
// (a newer hub) are kept.
export function setCategory(prefs, id, on) {
  const cur = prefs && Array.isArray(prefs.mute_categories) ? prefs.mute_categories : [];
  const rest = cur.filter((x) => x !== id);
  return on ? rest : [...rest, id];
}

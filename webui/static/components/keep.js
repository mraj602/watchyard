// keepLive: on a redraw of the same page, the element marked `data-keep` (the sessions search
// box, pwa.md 3.0b) stays the element it was, so typing, the caret, the focus and an on-screen
// keyboard's composing word survive a refresh of the list. Everything around it is the new
// page: at each level from the page down to it, the old element takes the new one's
// attributes and children, and the kept element never leaves the document (leaving it would
// blur it). Its value follows the new one only when the page changed it (Clear, Escape).
// The kept element and its ancestors keep their old event listeners, so a view gives those
// ancestors none and the kept element only handlers that read module state.
//
// Returns false, and changes nothing, when the old page has no kept element or the new page
// does not have it at the same depth under the same tags; the caller then swaps the pages.

export function keepLive(oldRoot, newRoot) {
  if (!oldRoot || !newRoot || oldRoot.tagName !== newRoot.tagName) return false;
  const live = oldRoot.querySelector('[data-keep]');
  if (!live) return false;
  const fresh = [...newRoot.querySelectorAll('[data-keep]')].find((el) => el.dataset.keep === live.dataset.keep);
  if (!fresh) return false;
  const a = chain(live, oldRoot);
  const b = chain(fresh, newRoot);
  if (!a || !b || a.length !== b.length || a.some((el, i) => el.tagName !== b[i].tagName)) return false;
  for (let i = a.length - 1; i >= 1; i--) {
    const old = a[i], neu = b[i], keepOld = a[i - 1], keepNew = b[i - 1];
    syncAttrs(old, neu);
    for (const c of [...old.childNodes]) if (c !== keepOld) c.remove();
    const kids = [...neu.childNodes];
    const at = kids.indexOf(keepNew);
    keepOld.before(...kids.slice(0, at));
    keepOld.after(...kids.slice(at + 1));
  }
  syncAttrs(live, fresh, 'value');
  if ('value' in live && live.value !== fresh.value) live.value = fresh.value;
  return true;
}

// The element and its ancestors up to root, innermost first; null when root is not one.
function chain(el, root) {
  const out = [el];
  while (el !== root) {
    el = el.parentElement;
    if (!el) return null;
    out.push(el);
  }
  return out;
}

// syncAttrs gives old the attributes of neu, except `skip` (the kept input's value attribute).
function syncAttrs(old, neu, skip) {
  for (const { name } of [...old.attributes]) if (name !== skip && !neu.hasAttribute(name)) old.removeAttribute(name);
  for (const { name, value } of [...neu.attributes]) if (name !== skip && old.getAttribute(name) !== value) old.setAttribute(name, value);
}

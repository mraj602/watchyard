// More (phone tab): Alerts, Machines, Switches, Settings, Peers, Analytics; the owner also gets Devices.

import { h } from '../components/dom.js';
import { isOwner, inApp } from '../store.js';
import { ownerHintNode } from '../components/ui.js';
import { plural } from '../format.js';
import { isMobile } from '../platform.js';

function item(href, label, sub, extra) {
  return h('li', {}, h('a', { class: 'more-link', href, ...(extra || {}) },
    h('span', { class: 'more-text' }, h('span', { class: 'strong' }, label), sub ? h('span', { class: 'muted small' }, sub) : null),
    h('span', { class: 'chev', 'aria-hidden': 'true' }, '›')));
}

export function render({ store }) {
  const owner = isOwner();
  const mobile = isMobile(navigator.userAgent, navigator.maxTouchPoints, navigator.platform);
  const node = h('div', { class: 'page' },
    h('header', { class: 'page-head' }, h('h1', {}, 'More')),
    h('ul', { class: 'panel more-list' },
      item('#/more/alerts', 'Alerts', 'Which notifications you get, and the recent ones'),
      item('#/switches', 'Switches', 'Every time a session moved to another account, and why'),
      item('#/machines', 'Machines', `${plural(store.overview.machines.length, 'machine')} reporting to this hub`),
      item('#/more/settings', 'Settings', `Notifications, this ${mobile ? 'phone' : 'device'}, app version`),
      item('#/more/peers', 'Peers', 'Which accounts’ sessions may message each other, and how to pause them'),
      item('#/more/keys', 'Keys', 'The owner key, and which machines are certified'),
      owner ? item('#/more/devices', 'Devices', 'Phones, browsers and machines paired with this hub; add a phone') : null,
      item('#/more/analytics', 'Analytics', 'Tokens and their cost at API prices, by day, model and project')),
    owner ? null : ownerHintNode({ app: inApp(), lead: 'Devices needs an owner sign-in.', key: 'more-owner-hint' }));
  return { title: 'More · Watchyard', node };
}

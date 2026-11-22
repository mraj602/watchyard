// Colours and spacing of the app's screens. Light and dark follow the system setting.
// The palette is the board's (webui/static/styles/base.css) and the icon's: ink #17191B and the
// gold accent #E8B04B, so the phone, the board and the launcher icon read as one product.
import { useColorScheme } from 'react-native';

export interface Theme {
  bg: string;
  card: string;
  text: string;
  muted: string;
  line: string;
  accent: string;
  accentText: string;
  ok: string;
  warn: string;
  bad: string;
  needs: string;
  track: string;
  /** Behind a found search term (the board's `mark`, --warn-bg). */
  mark: string;
}

export const LIGHT: Theme = {
  bg: '#F3F4F1', card: '#FFFFFF', text: '#17191B', muted: '#5E646B', line: '#DFE1DC', accent: '#E8B04B', accentText: '#17191B',
  ok: '#2E6B3A', warn: '#8A5A00', bad: '#B42318', needs: '#C2410C', track: '#E9EBE6', mark: '#FBF0D5',
};

export const DARK: Theme = {
  bg: '#121416', card: '#1B1E21', text: '#ECEEEF', muted: '#9AA1A8', line: '#2C3034', accent: '#E8B04B', accentText: '#17191B',
  ok: '#7CC48A', warn: '#E8B04B', bad: '#F0877D', needs: '#F08A57', track: '#2A2E32', mark: '#33290F',
};

export function useTheme(): Theme {
  return useColorScheme() === 'dark' ? DARK : LIGHT;
}

/** Switch colours: the accent track when on (React Native's default is a platform teal). */
export function switchColors(t: Theme): { trackColor: { false: string; true: string }; thumbColor: string } {
  return { trackColor: { false: t.line, true: t.accent }, thumbColor: '#FFFFFF' };
}

export const SPACE = { xs: 4, s: 8, m: 12, l: 16, xl: 24 } as const;

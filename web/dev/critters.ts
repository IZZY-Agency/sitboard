// Dev harness: every critter state at every size the page uses, for tuning
// the drawing. Served by `npm run dev` at /dev/critters.html; never part of the build.
import { mount } from 'svelte';
import Harness from './Harness.svelte';
import Sheet from './CritterSheet.svelte';
import { SCENARIOS } from './scenarios';
import { CATCHUPS } from './catchup';
import { LEFTOVERS } from './leftovers';
import { ISSUES } from './issues';
import '../src/styles/fonts.css';
import '../src/styles/tokens.css';
import '../src/styles/base.css';

mount(Harness, { target: document.getElementById('harness')!, props: { scenario: '', scenarios: SCENARIOS, live: false, catchup: CATCHUPS[0], catchups: CATCHUPS, leftover: LEFTOVERS[0], leftovers: LEFTOVERS, issue: ISSUES[0], issues: ISSUES } });
mount(Sheet, { target: document.getElementById('app')! });

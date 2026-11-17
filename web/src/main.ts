import { mount } from 'svelte';
import App from './App.svelte';
import { browserFeed } from './data/client';
import { createActions } from './data/actions';
import './styles/fonts.css';
import './styles/tokens.css';
import './styles/base.css';

const target = document.getElementById('app');
if (!target) throw new Error('missing #app');

export default mount(App, {
  target,
  props: { feed: browserFeed(), actions: createActions((...a) => fetch(...a)) },
});

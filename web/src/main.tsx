import { render } from 'preact';
import { App } from './components/App';
import './styles/tokens.css';
import './styles/base.css';
import './styles/app.css';

// The page's CSP (style-src 'self') blocks inline style attributes. Preact
// applies style objects through the CSSOM, which the CSP allows, so components
// pass computed styles as objects, never as strings.
const root = document.getElementById('app');
if (root === null) {
  throw new Error('index.html has no #app element');
}
render(<App />, root);

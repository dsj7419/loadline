import { render } from 'preact';
import './styles/tokens.css';
import './styles/base.css';

// The page's CSP (style-src 'self') blocks inline style attributes. Preact
// applies style objects through the CSSOM, which the CSP allows, so components
// pass computed styles as objects, never as strings.
function Placeholder() {
  return (
    <main style={{ display: 'grid', placeItems: 'center', minHeight: '100vh' }}>
      <h1 style={{ margin: 0, fontSize: '40px', fontWeight: 700, letterSpacing: '-0.02em', color: 'var(--accent)' }}>
        Loadline
      </h1>
    </main>
  );
}

const root = document.getElementById('app');
if (root === null) {
  throw new Error('index.html has no #app element');
}
render(<Placeholder />, root);

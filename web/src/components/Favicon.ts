// The tab icon is drawn at run time, so it can carry a dot while alerts are
// active. It is a data URL, which the page's CSP allows for images.

const size = 64;

// Fixed colours: the icon sits in the browser's tab strip, not on the page,
// so it does not follow the page's theme.
const tile = '#13161b';
const ink = '#e8eaed';
const accent = '#fb6a1c';

/**
 * Sets the tab icon to the Loadline mark, with an accent dot while alerts
 * are active. It does nothing in a browser that cannot draw on a canvas.
 */
export function drawFavicon(alerting: boolean): void {
  const canvas = document.createElement('canvas');
  canvas.width = size;
  canvas.height = size;
  const ctx = canvas.getContext('2d');
  if (ctx === null) {
    return;
  }

  ctx.fillStyle = tile;
  ctx.beginPath();
  ctx.roundRect(0, 0, size, size, 14);
  ctx.fill();

  // The load line: a ring with a bar through its middle.
  ctx.strokeStyle = ink;
  ctx.lineWidth = 6;
  ctx.beginPath();
  ctx.arc(32, 32, 17, 0, 2 * Math.PI);
  ctx.stroke();
  ctx.fillStyle = ink;
  ctx.beginPath();
  ctx.roundRect(6, 29, 52, 6, 3);
  ctx.fill();

  if (alerting) {
    ctx.fillStyle = accent;
    ctx.strokeStyle = tile;
    ctx.lineWidth = 5;
    ctx.beginPath();
    ctx.arc(50, 14, 11, 0, 2 * Math.PI);
    ctx.fill();
    ctx.stroke();
  }

  iconLink().href = canvas.toDataURL('image/png');
}

function iconLink(): HTMLLinkElement {
  const existing = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
  if (existing !== null) {
    return existing;
  }
  const link = document.createElement('link');
  link.rel = 'icon';
  document.head.append(link);
  return link;
}

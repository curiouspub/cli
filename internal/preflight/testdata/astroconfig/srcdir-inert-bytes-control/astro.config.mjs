const count = 3;
const total = count + 1 - 2;
const wide = count > 2 && total < 10 || false;
const mask = (count & 1) | (2 ^ 3);
const flip = ~count;
const not = !wide;
const idFn = (x) => x;
const tagged = { '@scope': '#anchor' };

export default {
  srcDir: './source',
};

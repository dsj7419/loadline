// main.tsx imports its stylesheets only for their side effects, and
// TypeScript checks that such imports resolve (noUncheckedSideEffectImports).
// The empty body exports nothing, so code can't read a value from one.
declare module '*.css' {}

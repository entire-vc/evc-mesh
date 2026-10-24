// Side-effect stylesheet import: esbuild bundles it into theme.css.
declare module "*.css";
declare module "*.json" {
  const value: unknown;
  export default value;
}

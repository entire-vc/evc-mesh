// Attributes that keep browser and extension password managers from treating
// a field as a login/password pair. Chrome ignores autoComplete="off" for
// saved logins, so secret fields use "new-password" and the search box is
// marked as a non-credential input.
export const CREDENTIAL_IGNORE_PROPS = {
  "data-1p-ignore": true,
  "data-bwignore": true,
} as const;

export const SECRET_INPUT_PROPS = {
  autoComplete: "new-password",
  ...CREDENTIAL_IGNORE_PROPS,
} as const;

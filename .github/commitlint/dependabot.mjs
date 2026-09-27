// Dependabot capitalises "Bump" after whatever prefix is configured, here
// "chore(deps): Bump ...", and that capital B fails config-conventional's
// subject-case. commitlint's ignores take functions only, which YAML cannot
// hold, so .commitlintrc.yml extends this file. The match needs Dependabot's
// subject AND its sign-off, so a human commit is not skipped by accident.
const subject = /^chore\(deps\): Bump /;
const signoff = /^Signed-off-by: dependabot\[bot\] <support@github\.com>$/m;

export default {
  ignores: [(message) => subject.test(message) && signoff.test(message)],
};

"use strict";
// semantic-release configuration in JS format
// We switch to .js format instead of yaml because we need to add js code for release-notes-generator plugin
// How do you test these changes?
// * Create a Github token and set it as GH_TOKEN env variable
// * Create a remote branch (e.g. fix/TIKI-108/dedup-release-notes)
// * Add some commits to the branch
// * Run semantic-release with debug mode as
// semantic-release -b main -b fix/TIKI-108/dedup-release-notes --ci false --dry-run --debug
module.exports = {
  branches: ["main"],
  ci: true,
  plugins: [
    ["@semantic-release/commit-analyzer", { preset: "conventionalcommits" }],
    [
      "@semantic-release/release-notes-generator",
      {
        writerOpts: {
          finalizeContext: function (context) {
            // We want to include only PR commits in the release notes
            // Our PRs might be including multiple commits,
            // but they don't necessarily have to be part of the release notes
            // This captures all commit messages which has (#[0-9]+) in the first line, which is the PR number.
            // It's important to note that semantic-release will only capture conventional commits.
            // see https://github.com/conventional-changelog/conventional-changelog/tree/master/packages/conventional-changelog-writer
            // also see https://www.conventionalcommits.org/
            const prRegexpMatch = /.*\(#\d+\).*/g;
            context.commitGroups.forEach((cg) => {
              cg.commits = cg.commits.filter((cm) =>
                prRegexpMatch.test(cm.header)
              );
            });
            context.commitGroups = context.commitGroups.filter(
              (cg) => cg.commits && cg.commits.length > 0
            );
            return context;
          },
        },
      },
    ],
    "@semantic-release/github",
  ],
};

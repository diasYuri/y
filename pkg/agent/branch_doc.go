package agent

// BranchManager and Branch implement light-weight transcript branching for
// agents that need to explore alternative tool-call paths or what-if
// dialogues without overwriting the main timeline.
//
// # When to branch
//
// Forks are useful when:
//
//   - Probing a destructive or expensive tool path while preserving the
//     option to fall back to the main transcript.
//   - Running multiple parallel reasoning attempts (e.g., self-consistency
//     sampling, agent debates) and merging or scoring them later.
//   - Maintaining a long-lived "main" timeline that is occasionally
//     extended by short scratch sessions.
//
// # Cost model
//
// Each branch is a copy of the parent transcript at fork time, so every
// fork costs O(N) memory in the size of the parent transcript. Tool
// results and assistant messages stored on the branch are deep copies
// (see cloneMessage) so that mutating one branch never affects another.
// Branches are kept entirely in memory; persisting them is the caller's
// responsibility (see [Agent.Snapshot] or roll your own serializer).
//
// # Integration with hooks
//
// BranchManager itself does not interact with the provider, the tool
// registry, or hooks: it only stores transcripts. The typical workflow is:
//
//  1. Use [BranchManager.Fork] to create a child branch.
//  2. Build a fresh [Agent] (or [Agent.Reset]) seeded with the child
//     branch via [WithTranscript].
//  3. Run the agent. Hooks ([WithBeforeRequest], [WithAfterRequest],
//     [WithBeforeToolCall], [WithAfterToolCall], [WithOnError]) fire
//     against that agent normally.
//  4. Copy the resulting [Agent.Transcript] back into the branch with
//     [BranchManager.SetMessages]; merge to the parent with
//     [BranchManager.Merge] when the experiment succeeds.
//
// Branches and [Agent.Snapshot] compose: snapshot the agent, fork the
// branch, restore the snapshot onto a second agent for the alternative
// path. This keeps observability (event sinks, hooks) attached to each
// agent independently.

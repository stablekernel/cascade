package generate

// correctnessCensus classifies every emitted-affecting manifest field (the
// union emittedAffectingCensus() computes from emittedFieldRegistry and
// emittedAffectingAllowlistMutators) by its generation-correctness coverage.
// TestCorrectnessCensus_EveryEmittedFieldClassified forces every census field
// into exactly one entry here; a new emitted-affecting field reds that guard
// until it is classified.
//
// An entry is one of:
//   - assertion: names a test in this package that pins the field's emitted
//     semantic shape (the field carries a distinct downstream contract).
//   - markerValidityOnly: the only contract is validity + round-trip, already
//     pinned by the T1 battery and the actionlint sweep (free scalar/glob/name
//     with no distinct downstream shape).
//   - markerStructural: the emitted shape is identical to a sibling's and is
//     pinned by that sibling's named assertion (note carries the Test name).
//   - markerNotEmitted: the supported value produces no emitted splice
//     (validated-only enum, reserved block, or the alternative is rejected).
//   - markerFollowup: emitted-affecting with a distinct semantic shape that
//     warrants a correctness assertion NOT YET written. The markerFollowup set
//     is the enumerated remaining-fields backlog for the next tranche.
var correctnessCensus = map[string]correctnessCoverage{
	// -- action folder / pins -------------------------------------------------
	"action_folder":    {marker: markerValidityOnly, note: "composite-action folder name; spliced into a filesystem path and uses:, validity + round-trip is the contract"},
	"action_pins.*":    {assertion: "TestGenCorrectness_ActionPins_UsesCarriesPinnedRef"},
	"action_pins[key]": {marker: markerStructural, note: "lookup key selecting a pinned action; the spliced ref is pinned by TestGenCorrectness_ActionPins_UsesCarriesPinnedRef"},

	// -- builds ---------------------------------------------------------------
	"builds[].artifacts[].name":            {marker: markerValidityOnly, note: "artifact name in emitted shell; validity + round-trip is the contract"},
	"builds[].artifacts[].path":            {marker: markerValidityOnly, note: "artifact glob in emitted shell; validity + round-trip is the contract"},
	"builds[].artifact_upload.upload":      {marker: markerValidityOnly, note: "passthrough upload glob; validity + round-trip is the contract"},
	"builds[].artifact_upload.downloads[]": {marker: markerValidityOnly, note: "passthrough download job name; validity + round-trip is the contract"},
	"builds[].inputs[key]":                 {marker: markerValidityOnly, note: "with: input key; validity + round-trip is the contract"},
	"builds[].inputs.*":                    {assertion: "TestGenerator_UnresolvedStateRefFailsGeneration"},
	"builds[].matrix.dimensions[key]":      {marker: markerValidityOnly, note: "strategy.matrix key + ${{ matrix.<k> }} deref; validity + round-trip is the contract"},
	"builds[].matrix.dimensions.*":         {marker: markerValidityOnly, note: "matrix value emitted via Go %q into a flow sequence; validity + round-trip is the contract"},
	"builds[].name":                        {assertion: "TestGenerator_FinalizeOutputKeysUseJobID"},
	"builds[].secrets.map[key]":            {assertion: "TestGenCorrectness_SecretsMap_PropagatesSourceToCallee"},
	"builds[].secrets.map.*":               {marker: markerStructural, note: "same secrets: block, pinned by TestGenCorrectness_SecretsMap_PropagatesSourceToCallee"},
	"builds[].triggers[]":                  {marker: markerValidityOnly, note: "paths-filter glob; validity + round-trip is the contract"},
	"builds[].workflow":                    {marker: markerValidityOnly, note: "reusable-workflow callback path spliced into uses:; validity + round-trip is the contract"},
	"builds[].depends_on[]":                {assertion: "TestGenCorrectness_DependsOn_SequencesButOnlyRequiredGates"},
	"builds[].optional_depends_on[]":       {assertion: "TestGenCorrectness_DependsOn_SequencesButOnlyRequiredGates"},
	"builds[].env_inputs[key]":             {marker: markerValidityOnly, note: "environment reference key; validity + round-trip is the contract"},
	"builds[].env_inputs.*":                {marker: markerValidityOnly, note: "env-routed JSON matrix payload; validity + round-trip is the contract"},
	"builds[].on_failure":                  {marker: markerNotEmitted, note: "abort (the only emittable value) adds no distinct output; continue is rejected at validation, pinned by TestActionlint_FeatureMatrix on_failure_continue_rejected"},
	"builds[].run_policy":                  {assertion: "TestGenCorrectness_RunPolicy_GatesCallbackIf"},
	"builds[].permissions[key]":            {assertion: "TestGenCorrectness_Permissions_LeastPrivilegePerJob"},
	"builds[].permissions.*":               {marker: markerStructural, note: "same permissions: block, pinned by TestGenCorrectness_Permissions_LeastPrivilegePerJob"},

	// -- changelog / cli ------------------------------------------------------
	"changelog.workflow": {marker: markerValidityOnly, note: "callback path spliced into uses:; validity + round-trip is the contract"},
	"cli_version":        {marker: markerValidityOnly, note: "setup-cli@<ref> splice; validity + round-trip is the contract"},
	"cli_version_sha":    {marker: markerValidityOnly, note: "40-hex SHA into setup-cli ref; validity + round-trip is the contract"},

	// -- components -----------------------------------------------------------
	"components[key]":            {marker: markerValidityOnly, note: "component name keying the per-component workflow set; validity + round-trip is the contract"},
	"components.*.path":          {marker: markerValidityOnly, note: "component subtree path -> paths-filter globs; validity + round-trip is the contract"},
	"components.*.extra_paths[]": {marker: markerValidityOnly, note: "extra paths-filter glob; validity + round-trip is the contract"},

	// -- concurrency ----------------------------------------------------------
	"concurrency.group": {assertion: "TestGenCorrectness_Concurrency_CancelInProgressHonored"},

	// -- deploys --------------------------------------------------------------
	"deploys[].artifact_upload.upload":      {marker: markerValidityOnly, note: "passthrough upload glob; validity + round-trip is the contract"},
	"deploys[].artifact_upload.downloads[]": {marker: markerValidityOnly, note: "passthrough download job name; validity + round-trip is the contract"},
	"deploys[].inputs[key]":                 {marker: markerValidityOnly, note: "with: input key; validity + round-trip is the contract"},
	"deploys[].inputs.*":                    {assertion: "TestPromoteGenerator_UnresolvedEnvStateRefStaysVisible"},
	"deploys[].name":                        {assertion: "TestGenerator_FinalizeOutputKeysUseJobID"},
	"deploys[].secrets.map[key]":            {marker: markerStructural, note: "shared writeSecretsBlock, pinned by TestGenCorrectness_SecretsMap_PropagatesSourceToCallee"},
	"deploys[].secrets.map.*":               {marker: markerStructural, note: "shared writeSecretsBlock, pinned by TestGenCorrectness_SecretsMap_PropagatesSourceToCallee"},
	"deploys[].triggers[]":                  {marker: markerValidityOnly, note: "paths-filter glob; validity + round-trip is the contract"},
	"deploys[].workflow":                    {marker: markerValidityOnly, note: "callback path spliced into uses:; validity + round-trip is the contract"},
	"deploys[].depends_on[]":                {assertion: "TestGM5_DependentDeploy_JudgesEffectiveResult"},
	"deploys[].optional_depends_on[]":       {assertion: "TestGenCorrectness_DependsOn_SequencesButOnlyRequiredGates"},
	"deploys[].env_inputs[key]":             {marker: markerValidityOnly, note: "environment reference key; validity + round-trip is the contract"},
	"deploys[].env_inputs.*":                {assertion: "TestPromoteGenerator_UnresolvedEnvStateRefStaysVisible"},
	"deploys[].on_failure":                  {marker: markerNotEmitted, note: "abort (the only emittable value) adds no distinct output; continue is rejected at validation"},
	"deploys[].run_policy":                  {assertion: "TestGenCorrectness_RunPolicy_GatesCallbackIf"},
	"deploys[].permissions[key]":            {marker: markerStructural, note: "per-job permissions contract (incl. the least-privilege omission) is pinned by TestGenCorrectness_Permissions_LeastPrivilegePerJob"},
	"deploys[].permissions.*":               {marker: markerStructural, note: "same permissions: block, pinned by TestGenCorrectness_Permissions_LeastPrivilegePerJob"},

	// -- dispatch inputs ------------------------------------------------------
	"dispatch_inputs[key]":          {assertion: "TestGenCorrectness_DispatchInputs_EmitsTypedInputBlock"},
	"dispatch_inputs.*.default":     {marker: markerStructural, note: "same workflow_dispatch input block, pinned by TestGenCorrectness_DispatchInputs_EmitsTypedInputBlock"},
	"dispatch_inputs.*.options[]":   {marker: markerStructural, note: "same workflow_dispatch input block, pinned by TestGenCorrectness_DispatchInputs_EmitsTypedInputBlock"},
	"dispatch_inputs.*.description": {marker: markerValidityOnly, note: "free operator help text emitted via Go %q; validity + round-trip is the contract"},
	"dispatch_inputs.*.type":        {marker: markerStructural, note: "input type emitted in the workflow_dispatch block, pinned by TestGenCorrectness_DispatchInputs_EmitsTypedInputBlock"},

	// -- environments ---------------------------------------------------------
	"environments[].name":                 {marker: markerValidityOnly, note: "environment name keying the promote ladder and job IDs; validity + round-trip is the contract"},
	"environments[].environment_url":      {assertion: "TestGenCorrectness_EnvironmentURL_EmitsPerEnvCase"},
	"environments[].secrets[]":            {marker: markerValidityOnly, note: "secret name in the emitted environment secrets payload; validity + round-trip is the contract"},
	"environments[].variables[]":          {marker: markerValidityOnly, note: "variable name in the emitted environment payload; validity + round-trip is the contract"},
	"environments[].role":                 {assertion: "TestGenCorrectness_EnvironmentRole_MovesReleaseStage"},
	"environments[].branch_policy":        {marker: markerValidityOnly, note: "environment-provisioning API JSON payload; generation tests presence only"},
	"environments[].gha_environment":      {marker: markerValidityOnly, note: "environment-provisioning API JSON payload; generation tests presence only"},
	"environments[].branch_patterns[]":    {marker: markerValidityOnly, note: "environment-provisioning API JSON payload; generation tests presence only"},
	"environments[].tag_patterns[]":       {marker: markerValidityOnly, note: "environment-provisioning API JSON payload; generation tests presence only"},
	"environments[].required_reviewers[]": {marker: markerValidityOnly, note: "environment-provisioning API JSON payload; generation tests presence only"},

	// -- external -------------------------------------------------------------
	"external[].ref":                                 {marker: markerValidityOnly, note: "git ref spliced into a cross-repo uses:@ref; validity + round-trip is the contract"},
	"external[].repo":                                {marker: markerValidityOnly, note: "runtime identity comparison; the emitted uses: path comes from the deploy workflow, validity + round-trip is the contract"},
	"external[].deploys[].name":                      {marker: markerValidityOnly, note: "external deploy job name; validity + round-trip is the contract"},
	"external[].deploys[].workflow":                  {marker: markerValidityOnly, note: "cross-repo callback path spliced into uses:; validity + round-trip is the contract"},
	"external[].deploys[].on_update.deploy.workflow": {marker: markerValidityOnly, note: "cross-repo callback path spliced into uses:; validity + round-trip is the contract"},
	"external[].deploys[].triggers[]":                {marker: markerValidityOnly, note: "paths-filter glob; validity + round-trip is the contract"},
	"external[].deploys[].secrets.map[key]":          {marker: markerStructural, note: "shared writeSecretsBlock, pinned by TestGenCorrectness_SecretsMap_PropagatesSourceToCallee"},
	"external[].deploys[].secrets.map.*":             {marker: markerStructural, note: "shared writeSecretsBlock, pinned by TestGenCorrectness_SecretsMap_PropagatesSourceToCallee"},
	"external[].deploys[].optional_depends_on[]":     {marker: markerFollowup, note: "external optional needs: edge gating is not pinned; the same-repo optional sequencing-not-gating contract is pinned by TestGenCorrectness_DependsOn_SequencesButOnlyRequiredGates, but the external-deploy emit path is distinct and not yet asserted"},
	"external[].deploys[].permissions[key]":          {marker: markerStructural, note: "per-job permissions contract pinned by TestGenCorrectness_Permissions_LeastPrivilegePerJob"},
	"external[].deploys[].permissions.*":             {marker: markerStructural, note: "same permissions: block, pinned by TestGenCorrectness_Permissions_LeastPrivilegePerJob"},

	// -- extra triggers -------------------------------------------------------
	"extra_triggers.schedule[].cron":             {assertion: "TestGenCorrectness_ExtraTriggers_EmitsScheduleAndDispatch"},
	"extra_triggers.repository_dispatch.types[]": {marker: markerStructural, note: "same on: trigger set, pinned by TestGenCorrectness_ExtraTriggers_EmitsScheduleAndDispatch"},
	"extra_triggers.workflow_run.types[]":        {marker: markerStructural, note: "same on: trigger set, pinned by TestGenCorrectness_ExtraTriggers_EmitsScheduleAndDispatch"},
	"extra_triggers.workflow_run.workflows[]":    {marker: markerStructural, note: "same on: trigger set, pinned by TestGenCorrectness_ExtraTriggers_EmitsScheduleAndDispatch"},

	// -- git ------------------------------------------------------------------
	"git.mode":           {marker: markerStructural, note: "mode: custom triggers the git identity splice, pinned by TestGenCorrectness_GitCustomUser_SplicesNameAndEmail"},
	"git.user_name":      {assertion: "TestGenCorrectness_GitCustomUser_SplicesNameAndEmail"},
	"git.user_email":     {marker: markerStructural, note: "same git config splice, pinned by TestGenCorrectness_GitCustomUser_SplicesNameAndEmail"},
	"git.gpg_key_id":     {assertion: "TestGenCorrectness_GPGSigning_WiresImportAndSigningKey"},
	"git.gpg_key_secret": {assertion: "TestGenCorrectness_GPGSigning_WiresImportAndSigningKey"},

	// -- manifest addressing --------------------------------------------------
	"manifest_file": {assertion: "TestReconcileGenerator_EmitsManifestFlags"},
	"manifest_key":  {marker: markerStructural, note: "emitted --manifest-key flag pinned by TestReconcileGenerator_EmitsManifestFlags"},

	// -- notify ---------------------------------------------------------------
	"notify.repo":        {marker: markerValidityOnly, note: "owner/repo splice; validity + round-trip is the contract"},
	"notify.workflow":    {marker: markerValidityOnly, note: "single-quoted github-script literal; validity + round-trip is the contract"},
	"notify.deploy_name": {marker: markerValidityOnly, note: "single-quoted github-script literal; validity + round-trip is the contract"},
	"notify.environment": {marker: markerValidityOnly, note: "single-quoted github-script literal; validity + round-trip is the contract"},
	"notify.token":       {marker: markerValidityOnly, note: "token expression into an unquoted YAML scalar; validity + round-trip is the contract"},

	// -- publish / release ----------------------------------------------------
	"publish.workflow":              {marker: markerValidityOnly, note: "callback path spliced into uses:; validity + round-trip is the contract"},
	"release_build.workflow":        {marker: markerValidityOnly, note: "callback path spliced into uses:; validity + round-trip is the contract"},
	"release_build.tag":             {assertion: "TestGenerator_ExternalReleaseDotlessTagErrors"},
	"release_token":                 {marker: markerValidityOnly, note: "token expression into an unquoted YAML scalar; validity + round-trip is the contract"},
	"state_token":                   {marker: markerValidityOnly, note: "token expression into an unquoted YAML scalar; validity + round-trip is the contract"},
	"release_token_app.app_id":      {marker: markerValidityOnly, note: "secret reference expression; validity + round-trip is the contract"},
	"release_token_app.private_key": {marker: markerValidityOnly, note: "secret reference expression; validity + round-trip is the contract"},
	"state_token_app.app_id":        {marker: markerValidityOnly, note: "secret reference expression; validity + round-trip is the contract"},
	"state_token_app.private_key":   {marker: markerValidityOnly, note: "secret reference expression; validity + round-trip is the contract"},

	// -- rollback -------------------------------------------------------------
	"rollback.repository_dispatch.types[]": {assertion: "TestGM4_RollbackDispatch_DryRunTreatsBooleanAndString"},

	// -- shared paths / tag grammar -------------------------------------------
	"shared_paths[]":                   {marker: markerValidityOnly, note: "paths-filter glob; validity + round-trip is the contract"},
	"tag_grammar.prefix":               {marker: markerValidityOnly, note: "tag prefix in argv; validity + round-trip is the contract"},
	"tag_grammar.prerelease_token":     {marker: markerValidityOnly, note: "tag token component; validity + round-trip is the contract"},
	"tag_grammar.prerelease_separator": {marker: markerValidityOnly, note: "tag token component; validity + round-trip is the contract"},
	"tag_grammar.dryrun_token":         {marker: markerValidityOnly, note: "tag token component; validity + round-trip is the contract"},

	// -- top-level triggers / trunk -------------------------------------------
	"triggers[]":   {marker: markerValidityOnly, note: "paths-filter glob; validity + round-trip is the contract"},
	"trunk_branch": {assertion: "TestGenerate_EmptyPushBranchList_NeverEmitted"},

	// -- validate -------------------------------------------------------------
	"validate.workflow":         {marker: markerValidityOnly, note: "callback path spliced into uses:; validity + round-trip is the contract"},
	"validate.triggers[]":       {marker: markerValidityOnly, note: "paths-filter glob; validity + round-trip is the contract"},
	"validate.inputs[key]":      {marker: markerValidityOnly, note: "with: input key; validity + round-trip is the contract"},
	"validate.inputs.*":         {marker: markerValidityOnly, note: "with: input value; validity + round-trip is the contract"},
	"validate.env_inputs[key]":  {marker: markerValidityOnly, note: "environment reference key; validity + round-trip is the contract"},
	"validate.env_inputs.*":     {marker: markerValidityOnly, note: "env-routed JSON matrix payload; validity + round-trip is the contract"},
	"validate.secrets.map[key]": {marker: markerStructural, note: "shared writeSecretsBlock, pinned by TestGenCorrectness_SecretsMap_PropagatesSourceToCallee"},
	"validate.secrets.map.*":    {marker: markerStructural, note: "shared writeSecretsBlock, pinned by TestGenCorrectness_SecretsMap_PropagatesSourceToCallee"},
	"validate.on_failure":       {marker: markerNotEmitted, note: "abort (the only emittable value) adds no distinct output; continue is rejected at validation"},
	"validate.run_policy":       {assertion: "TestGenCorrectness_RunPolicy_GatesCallbackIf"},
	"validate.permissions[key]": {marker: markerStructural, note: "per-job permissions contract pinned by TestGenCorrectness_Permissions_LeastPrivilegePerJob"},
	"validate.permissions.*":    {marker: markerStructural, note: "same permissions: block, pinned by TestGenCorrectness_Permissions_LeastPrivilegePerJob"},

	// -- generator-level selectors --------------------------------------------
	"pin_mode":         {assertion: "TestGenCorrectness_PinMode_ShaEmitsShaRefs"},
	"release_trigger":  {assertion: "TestGenCorrectness_ReleaseTrigger_DispatchDropsPush"},
	"reconcile.source": {marker: markerNotEmitted, note: "dependabot is the only valid value (validateReconcile) and has no emitted consumer in the generate package: enabling reconcile emits the companion, but source selects nothing distinct in the emitted output today, so there is no shape to pin"},
	"reconcile.commit": {assertion: "TestGenCorrectness_ReconcileCommit_AppendVsFollowup"},
}

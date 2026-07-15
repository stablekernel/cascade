package visualize

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// primaryGroupID and primaryGroupLabel identify the lane that holds the primary
// repo's own pipeline in the cross-repo projection. The id is a fixed slug rather
// than a repo name because the manifest does not carry the primary's own repo
// identity; "primary" reads clearly next to the named dependent lanes.
const (
	primaryGroupID    = "primary"
	primaryGroupLabel = "primary"
)

// BuildCrossRepoViewModel projects the manifest's cross-repo coordination into a
// render-agnostic flowchart. The primary repo's lifecycle pipeline forms the root
// lane; each dependent satellite the primary coordinates (cfg.External) becomes
// its own lane holding a node per external deployable, with an edge from the
// primary's pipeline to each one labeled with the deploy it drives; and a notify
// config (cfg.Notify) adds an upstream-primary lane with a notify edge back to
// it. A manifest with no external coordination and no notify renders just the
// primary pipeline with no lanes, so the projection stays sensible for the common
// single-repo case. The model carries no diagram syntax; the emitter draws the
// lanes and the cross-repo edges.
func BuildCrossRepoViewModel(cfg *config.TrunkConfig) (ViewModel, error) {
	if cfg == nil {
		return ViewModel{}, fmt.Errorf("visualize: nil config")
	}

	vm := ViewModel{Kind: DiagramFlowchart}

	// The primary lane is the manifest's own lifecycle pipeline. primaryExit is
	// the last stage; cross-repo edges originate there because coordination and
	// notification happen once the primary's pipeline has run.
	var primaryNodeIDs []string
	var prev, primaryExit string
	for _, s := range presentStages(cfg) {
		vm.Nodes = append(vm.Nodes, Node{ID: s.id, Label: s.label, Kind: NodeStage})
		primaryNodeIDs = append(primaryNodeIDs, s.id)
		if prev != "" {
			vm.Edges = append(vm.Edges, Edge{From: prev, To: s.id, Kind: EdgeStage})
		}
		prev = s.id
		primaryExit = s.id
	}

	hasExternal := len(cfg.External) > 0
	hasNotify := cfg.Notify != nil && cfg.Notify.Repo != ""

	// No cross-repo relationships: render the primary pipeline alone, with no
	// lanes, so the diagram matches the single-repo stages view.
	if !hasExternal && !hasNotify {
		return vm, nil
	}

	// Once dependent or upstream lanes exist, the primary's own pipeline is drawn
	// as a lane so a reader can tell which nodes belong to which repo.
	vm.Groups = append(vm.Groups, Group{ID: primaryGroupID, Label: primaryGroupLabel, NodeIDs: primaryNodeIDs})

	for _, ext := range cfg.External {
		slug := repoSlug(ext.Repo)
		var memberIDs []string
		for _, d := range ext.Deploys {
			id := "ext_" + slug + "_" + repoSlug(d.Name)
			vm.Nodes = append(vm.Nodes, Node{ID: id, Label: d.Name, Kind: NodeDeploy})
			memberIDs = append(memberIDs, id)
			vm.Edges = append(vm.Edges, Edge{From: primaryExit, To: id, Kind: EdgeExternal, Label: d.Name})
		}
		// A dependent that declares no deployables still renders as a lane with a
		// single repo node, so the coordination relationship stays visible.
		if len(ext.Deploys) == 0 {
			id := "ext_" + slug
			vm.Nodes = append(vm.Nodes, Node{ID: id, Label: ext.Repo, Kind: NodeRepo})
			memberIDs = append(memberIDs, id)
			vm.Edges = append(vm.Edges, Edge{From: primaryExit, To: id, Kind: EdgeExternal, Label: ext.Repo})
		}
		vm.Groups = append(vm.Groups, Group{ID: "repo_" + slug, Label: ext.Repo, NodeIDs: memberIDs})
	}

	if hasNotify {
		slug := repoSlug(cfg.Notify.Repo)
		id := "notify_" + slug
		vm.Nodes = append(vm.Nodes, Node{ID: id, Label: cfg.Notify.Repo, Kind: NodeRepo})
		vm.Edges = append(vm.Edges, Edge{From: primaryExit, To: id, Kind: EdgeNotify, Label: "notify"})
		vm.Groups = append(vm.Groups, Group{ID: "repo_" + slug, Label: cfg.Notify.Repo, NodeIDs: []string{id}})
	}

	return vm, nil
}

// repoSlug folds a repository reference (for example "org/cdk-infra") into a
// Mermaid-safe identifier fragment by replacing every rune that is not a letter
// or digit with an underscore. Folding is lossy ("org/api-v2" and "org/api.v2"
// fold identically), so whenever any rune was folded a short content-hash
// suffix keeps the slug injective; without it two distinct repos would merge
// into one lane and one would silently disappear from the diagram. The
// human-facing repo string is kept on the node or group label, so only the
// identity token is slugged.
func repoSlug(repo string) string {
	var b strings.Builder
	b.Grow(len(repo))
	folded := false
	for _, r := range repo {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
			folded = true
		}
	}
	if !folded {
		return b.String()
	}
	sum := sha256.Sum256([]byte(repo))
	return b.String() + "_" + hex.EncodeToString(sum[:4])
}

// Package catalog is the hand-written data `monoagentcli ref` renders: the node catalogue
// (NodeDocs, one nodes_<group>.go file per category group) and the `ref commands` catalogue
// (CommandDocs, one commands_<group>.go file per command group). The CLI hands both to
// internal/cli/ref through its Deps.
package catalog

import (
	"strings"

	"github.com/monoes/mono-agent/internal/cli/ref"
)

type (
	nodeDoc = ref.NodeDoc
	cmdDoc  = ref.CmdDoc
)

// NodeDocs is the node catalogue, in catalogue order: `ref nodes` lists a category's types in
// it. `ref node` layers an entry over the type's schema, so a type with no entry still gets a
// generated page.
var NodeDocs = buildNodeDocs()

// CommandDocs is the `ref commands` catalogue, in order.
var CommandDocs = joinCommandDocs(
	cliDocs, dbCommandDocs, accountCommandDocs, integrationCommandDocs,
	libraryCommandDocs, taskCommandDocs, taskOSCommandDocs,
)

func buildNodeDocs() []nodeDoc {
	docs := joinNodeDocs(
		nodeDocsPublication, nodeDocsTrigger, nodeDocsSystem, nodeDocsCore,
		nodeDocsHttp, nodeDocsData, nodeDocsImage, nodeDocsVault, nodeDocsDb,
		nodeDocsComm, nodeDocsService, nodeDocsAi, nodeDocsGemini,
		nodeDocsInstagram, nodeDocsLinkedin, nodeDocsX, nodeDocsTiktok,
		nodeDocsApplications, nodeDocsPeople,
	)
	docs = append(docs, dataFilesNodeDocs...)
	docs = upsertNodeDocs(docs, dataMoreNodeDocs...)
	docs = upsertNodeDocs(docs, dataFilesDocOverrides...)
	docs = upsertNodeDocs(docs, dataFormatsDocOverrides...)
	docs = upsertNodeDocs(docs, dbNodeDocs...)
	docs = append(docs, imageEditNodeDocs...)
	docs = append(docs, moreNodeDocs...)
	docs = append(docs, moreServiceNodeDocs...)
	docs = append(docs, vectorNodeDocs...)
	return docs
}

func joinNodeDocs(parts ...[]nodeDoc) []nodeDoc {
	var all []nodeDoc
	for _, p := range parts {
		all = append(all, p...)
	}
	return all
}

func joinCommandDocs(parts ...[]cmdDoc) []cmdDoc {
	var all []cmdDoc
	for _, p := range parts {
		all = append(all, p...)
	}
	return all
}

// upsertNodeDocs replaces the entry of each doc's Type in docs, or appends the doc when the
// type has none yet.
func upsertNodeDocs(docs []nodeDoc, add ...nodeDoc) []nodeDoc {
	for _, d := range add {
		replaced := false
		for i := range docs {
			if docs[i].Type == d.Type {
				docs[i] = d
				replaced = true
				break
			}
		}
		if !replaced {
			docs = append(docs, d)
		}
	}
	return docs
}

// FindNodeDoc returns a type's entry in NodeDocs (matched case-insensitively), else the entry
// generated for an API package node (app.*), else nil.
func FindNodeDoc(typ string) *nodeDoc {
	for i := range NodeDocs {
		if strings.EqualFold(NodeDocs[i].Type, typ) {
			return &NodeDocs[i]
		}
	}
	return appNodeDoc(typ)
}

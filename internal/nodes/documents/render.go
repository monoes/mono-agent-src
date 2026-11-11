// Package documentsnodes exposes internal/documents as a workflow node
// type: documents.render.
package documentsnodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/documents"
	"github.com/monoes/mono-agent/internal/nodes/nodeprofile"
	"github.com/monoes/mono-agent/internal/workflow"
)

// globalDB is the process-wide SQLite DB used by RenderNode. Set at startup.
var globalDB *sql.DB

// SetGlobalDB wires the shared SQLite connection into this package's node(s).
func SetGlobalDB(db *sql.DB) {
	globalDB = db
}

// RegisterAll registers documents.render into the registry.
func RegisterAll(r *workflow.NodeTypeRegistry, db *sql.DB) {
	SetGlobalDB(db)
	r.Register("documents.render", func() workflow.NodeExecutor { return &RenderNode{} })
}

// RenderNode generates a CV/cover-letter/tender-proposal document from
// config and saves it into the vault.
// Type: "documents.render"
type RenderNode struct{}

func (n *RenderNode) Type() string { return "documents.render" }

// PublishesToThirdParty: documents.render sends nothing off this machine (issue #1418).
func (RenderNode) PublishesToThirdParty() bool { return false }

// WritesLocalStore: documents.render puts what it is given where another run can read it back (issue #1418).
func (RenderNode) WritesLocalStore() bool { return true }

func (n *RenderNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if globalDB == nil {
		return nil, fmt.Errorf("documents.render: database not available (call SetGlobalDB at startup)")
	}
	docTypeStr, _ := config["doc_type"].(string)
	rawData, ok := config["data"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("documents.render: config \"data\" must be an object")
	}
	dataJSON, err := json.Marshal(rawData)
	if err != nil {
		return nil, fmt.Errorf("documents.render: re-marshaling data: %w", err)
	}

	profileID, err := nodeprofile.Require(ctx, "documents.render", config)
	if err != nil {
		return nil, err
	}
	applicationID, _ := config["application_id"].(string)

	docType := documents.DocType(docTypeStr)
	var typedData interface{}
	switch docType {
	case documents.DocTypeCV:
		var d documents.CVData
		if err := json.Unmarshal(dataJSON, &d); err != nil {
			return nil, fmt.Errorf("documents.render: parsing data as CVData: %w", err)
		}
		typedData = d
	case documents.DocTypeCoverLetter:
		var d documents.CoverLetterData
		if err := json.Unmarshal(dataJSON, &d); err != nil {
			return nil, fmt.Errorf("documents.render: parsing data as CoverLetterData: %w", err)
		}
		typedData = d
	case documents.DocTypeTenderProposal:
		var d documents.TenderProposalData
		if err := json.Unmarshal(dataJSON, &d); err != nil {
			return nil, fmt.Errorf("documents.render: parsing data as TenderProposalData: %w", err)
		}
		typedData = d
	default:
		return nil, fmt.Errorf("documents.render: config \"doc_type\" must be one of cv, cover_letter, tender_proposal, got %q", docTypeStr)
	}

	htmlDocID, pdfDocID, err := documents.GenerateDocument(ctx, globalDB, profileID, applicationID, docType, typedData)
	if err != nil {
		return nil, fmt.Errorf("documents.render: %w", err)
	}

	out := map[string]interface{}{"html_document_id": htmlDocID, "pdf_document_id": pdfDocID, "doc_type": docTypeStr}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(out)}}}, nil
}

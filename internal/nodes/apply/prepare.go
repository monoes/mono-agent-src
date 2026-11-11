// internal/nodes/apply/prepare.go

// Package applynodes exposes internal/apply as a workflow node type:
// applications.prepare. Deliberately does not expose OpenForApplication
// as a node — an unattended workflow should never pop open an interactive
// browser window as an automated side effect.
package applynodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/apply"
	"github.com/monoes/mono-agent/internal/documents"
	"github.com/monoes/mono-agent/internal/nodes/nodeprofile"
	"github.com/monoes/mono-agent/internal/workflow"
)

var globalDB *sql.DB

// SetGlobalDB wires the shared SQLite connection into this package's node(s).
func SetGlobalDB(db *sql.DB) {
	globalDB = db
}

// RegisterAll registers applications.prepare into the registry.
func RegisterAll(r *workflow.NodeTypeRegistry, db *sql.DB) {
	SetGlobalDB(db)
	r.Register("applications.prepare", func() workflow.NodeExecutor { return &PrepareNode{} })
}

// PrepareNode generates (or reuses) an application's CV and cover letter
// via apply.Prepare.
// Type: "applications.prepare"
type PrepareNode struct{}

func (n *PrepareNode) Type() string { return "applications.prepare" }

// PublishesToThirdParty: applications.prepare sends nothing off this machine (issue #1418).
func (PrepareNode) PublishesToThirdParty() bool { return false }

func (n *PrepareNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if globalDB == nil {
		return nil, fmt.Errorf("applications.prepare: database not available (call SetGlobalDB at startup)")
	}
	applicationID, _ := config["application_id"].(string)
	if applicationID == "" {
		return nil, fmt.Errorf("applications.prepare: config \"application_id\" is required")
	}
	profileID, err := nodeprofile.Require(ctx, "applications.prepare", config)
	if err != nil {
		return nil, err
	}

	cvRaw, _ := config["cv_data"].(map[string]interface{})
	letterRaw, _ := config["cover_letter_data"].(map[string]interface{})

	var cvData documents.CVData
	if cvJSON, err := json.Marshal(cvRaw); err == nil {
		_ = json.Unmarshal(cvJSON, &cvData)
	}
	var letterData documents.CoverLetterData
	if letterJSON, err := json.Marshal(letterRaw); err == nil {
		_ = json.Unmarshal(letterJSON, &letterData)
	}

	cvHTML, cvPDF, letterHTML, letterPDF, err := apply.Prepare(ctx, globalDB, profileID, applicationID, cvData, letterData)
	if err != nil {
		return nil, fmt.Errorf("applications.prepare: %w", err)
	}

	out := map[string]interface{}{
		"application_id":      applicationID,
		"cv_html_document_id": cvHTML, "cv_pdf_document_id": cvPDF,
		"cover_letter_html_document_id": letterHTML, "cover_letter_pdf_document_id": letterPDF,
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(out)}}}, nil
}

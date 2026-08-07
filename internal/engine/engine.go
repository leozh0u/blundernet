// Package engine picks moves for the bot side. The real engine runs the
// exported BlunderNet ONNX model; a small material searcher stands in when
// the model or ONNX Runtime library is unavailable (CI, fresh clones).
package engine

import (
	"log/slog"
	"os"
)

type Engine interface {
	// BestMove returns a UCI move for the side to move in fen.
	BestMove(fen string) (string, error)
	Name() string
}

// NewFromEnv builds the strongest engine the environment supports.
// MODEL_PATH points at the .onnx file; ONNXRUNTIME_LIB at the shared
// library. Both have sensible defaults for local dev.
func NewFromEnv() Engine {
	modelPath := envOr("MODEL_PATH", "models/blundernet.onnx")
	if _, err := os.Stat(modelPath); err != nil {
		slog.Warn("model not found, using material fallback", "path", modelPath)
		return NewMaterial()
	}
	onnx, err := NewONNX(modelPath)
	if err != nil {
		slog.Warn("onnx unavailable, using material fallback", "err", err)
		return NewMaterial()
	}
	var eng Engine = onnx
	if sims := SimsFromEnv(); sims > 1 {
		eng = NewMCTS(onnx, sims)
	}
	slog.Info("engine selected", "engine", eng.Name())
	return eng
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

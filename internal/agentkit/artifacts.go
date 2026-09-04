package agentkit

import "path/filepath"

// ArtifactManager 管理 batch 和项目产物路径。
type ArtifactManager struct {
	BaseDir string
}

// NewArtifactManager 创建产物路径管理器。
func NewArtifactManager(baseDir string) ArtifactManager {
	if baseDir == "" {
		baseDir = "data"
	}
	return ArtifactManager{BaseDir: filepath.Clean(baseDir)}
}

// BatchDir 返回 batch 的根产物目录。
func (m ArtifactManager) BatchDir(batchID string) string {
	return filepath.Join(m.BaseDir, "batches", batchID)
}

// BatchProjectsDir 返回 batch 下项目产物目录。
func (m ArtifactManager) BatchProjectsDir(batchID string) string {
	return filepath.Join(m.BatchDir(batchID), "projects")
}

// ProjectDir 返回 batch 中单个项目的产物目录。
func (m ArtifactManager) ProjectDir(batchID string, projectID string) string {
	return filepath.Join(m.BatchProjectsDir(batchID), projectID)
}

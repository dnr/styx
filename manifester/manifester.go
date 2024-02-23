package manifester

type (
	server struct {
		cfg *Config
	}

	Config struct {
		Bind     string
		Upstream string
		// One of these is required:
		ChunkBucket   string
		ChunkLocalDir string
	}
)

func ManifestServer(cfg Config) *server {
	return &server{
		cfg: &cfg,
	}
}

func (s *server) Run() error {
	return nil
}

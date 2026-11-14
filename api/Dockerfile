# Build from the repo root: docker build -f api/Dockerfile .
# The root context lets the build see ../jobstore (go.mod replace) and the shared config.
FROM golang:1.27.1

WORKDIR /src

# Copy module files first so dependency downloads are cached
COPY jobstore/ ./jobstore/
COPY api/go.mod api/go.sum ./api/
RUN cd api && go mod download

# Copy the cmd/ and internal/ source trees
COPY api/cmd ./api/cmd
COPY api/internal ./api/internal

# Build our code and output the executable go code into /api
RUN cd api && CGO_ENABLED=0 GOOS=linux go build -o /api ./cmd/api

# Shared contracts the API reads at startup (ESTIMATES_PATH, DATASETS_CSV defaults)
WORKDIR /app
COPY config/ ./config/
COPY datasets/ ./datasets/

# Expose our port
EXPOSE 8080

# Run the executable
CMD ["/api"]

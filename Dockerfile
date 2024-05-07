FROM golang:1.22.2-bookworm

# Set work directory
WORKDIR /app

# Install Kurtosis CLI using the provided installation instructions
RUN echo "deb [trusted=yes] https://apt.fury.io/kurtosis-tech/ /" > /etc/apt/sources.list.d/kurtosis.list \
    && apt-get update \
    && apt-get install -y kurtosis-cli

# Copy the local code to the container's workspace
COPY . .

# Build the Go app
RUN go build -o server ./cmd/server

# Expose the port the server listens on
EXPOSE 8080

# Command to run the server
CMD ["./server"]
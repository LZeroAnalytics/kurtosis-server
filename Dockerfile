FROM golang:1.22.2-bookworm

# Set work directory
WORKDIR /app

# Copy the local code to the container's workspace
COPY . .

# Build the Go app
RUN go build -o server ./cmd/server

# Expose the port the server listens on
EXPOSE 8080

# Command to run the server
CMD ./server
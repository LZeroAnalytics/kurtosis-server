FROM golang:1.22.2-bookworm

# Set work directory
WORKDIR /app

# Install packages
RUN apt-get update && apt-get install -y curl gnupg software-properties-common unzip \
    && echo "deb [trusted=yes] https://apt.fury.io/kurtosis-tech/ /" > /etc/apt/sources.list.d/kurtosis.list \
    && apt-get update \
    && apt-get install -y kurtosis-cli \
    && curl -s https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip -o awscliv2.zip \
    && unzip awscliv2.zip \
    && ./aws/install \
    && curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" \
    && install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl

# Copy the local code to the container's workspace
COPY . .

# Build the Go app
RUN go build -o server ./cmd/server

# Expose the port the server listens on
EXPOSE 8080

# Command to run the server
CMD ./server
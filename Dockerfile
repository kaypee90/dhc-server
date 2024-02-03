# Use the official Golang image as the base image
FROM golang:latest

# Set the working directory inside the container
WORKDIR /app

# Copy the local code to the container
COPY . .

# Download and install any dependencies
RUN go mod download

# Build the Go application
RUN go build -o main .

# Expose the port your application will run on
EXPOSE 8080

# Command to run the executable
CMD ["./main"]

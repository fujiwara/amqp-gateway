{
  amqp_url: "amqp://localhost:5672",
  listen_addr: ":8080",
  aliases: [
    {
        method: "rpc",
        path: "/api/upper",
        username: "guest",
        password: "guest",
        exchange: "request",
        routing_key: "upper",
    },
  ],
}

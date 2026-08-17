# gRPC — Theory Notes

Written while building the `crm-service` prototype in this repository.

---

## 1. What is gRPC?

**gRPC** is a framework for **R**emote **P**rocedure **C**alls: it lets a program call a
function that lives in another process, usually on another machine, as if it
were a local function call. The "g" originally stood for Google, where it was
developed; it is now an open, CNCF-governed project.

The defining idea is **contract-first development**. You do not start by writing
a server and documenting it afterwards. You start by writing a formal contract —
a `.proto` file — that declares the available methods and the exact shape of
every message. A code generator then reads that contract and produces, for each
language you need:

- data types for every message,
- a **client** object with real, callable methods,
- a **server interface** for you to implement.

Because both sides are generated from the same file, they cannot silently drift
apart. If the contract changes in an incompatible way, the code stops compiling.

In this repository the contract is [`proto/customerpb/customer.proto`](proto/customerpb/customer.proto):

```proto
service CustomerService {
  rpc CreateCustomer(CreateCustomerRequest) returns (CreateCustomerResponse);
  rpc GetCustomer(GetCustomerRequest) returns (GetCustomerResponse);
}
```

From those four lines the generator produced roughly 400 lines of Go. The only
code actually written by hand is the body of two methods.

gRPC rests on two technologies, described next: **Protocol Buffers** for
encoding data, and **HTTP/2** for transport.

---

## 2. Protocol Buffers (the data format)

Protocol Buffers ("protobuf") is a binary serialization format. It is the
default payload encoding for gRPC and it replaces JSON.

The key difference from JSON is that protobuf does **not transmit field names**.
Each field is assigned a permanent number in the contract, and only that number
travels on the wire:

```proto
message CreateCustomerRequest {
  string name  = 1;   // "1" is the field's wire identifier
  string email = 2;   // it is NOT a default value or an array index
}
```

JSON must send `{"name":"Ali","email":"ali@example.com"}` — around 40 bytes of
text that the receiver has to parse character by character. Protobuf sends the
same information in roughly half the space, and decoding is a direct binary
read rather than text parsing.

Three consequences follow:

**It is smaller and faster.** Irrelevant for one request; significant for
services exchanging thousands of messages per second.

**It is not human-readable.** You cannot `curl` a gRPC endpoint and read the
answer. Debugging requires tooling such as `grpcurl`.

**Field numbers are permanent.** Renaming `name` to `full_name` is safe, because
the name never travels. Changing or reusing a number is **not** safe: an older
peer will read the new data using the old meaning and misinterpret it silently,
with no error. The rule is: add new numbers, never recycle retired ones.

This is also what makes protobuf good at versioning. Unknown fields are ignored
rather than rejected, so a new server can add fields without breaking old
clients.

---

## 3. HTTP/2 (the transport)

gRPC requires HTTP/2, whereas REST APIs commonly run on HTTP/1.1. This provides:

- **Multiplexing** — many concurrent calls share a single TCP connection. Under
  HTTP/1.1 a connection handles one request at a time, so clients open several
  connections and still queue behind slow responses ("head-of-line blocking").
- **Binary framing** — the protocol itself is binary, not text.
- **Header compression** (HPACK) — repeated metadata is not re-sent in full.
- **Streaming** — a single call can carry a sequence of messages in either
  direction, which is what makes the streaming RPC types below possible.

The cost of this dependency is browser support. Browsers do not expose enough
low-level control over HTTP/2 for a page to speak gRPC directly, so a browser
client needs a translation layer (**gRPC-Web**) plus a proxy such as Envoy.
This single fact drives most architectural decisions about where gRPC belongs.

---

## 4. The four types of RPC

| Type | Shape | Example use |
|---|---|---|
| **Unary** | 1 request → 1 response | `CreateCustomer` — the normal case |
| **Server streaming** | 1 request → *n* responses | export all customers, streamed as found |
| **Client streaming** | *n* requests → 1 response | bulk import, uploading a large file |
| **Bidirectional streaming** | *n* ↔ *n*, independently | live chat, telemetry, notifications |

Streaming is declared with the `stream` keyword in the contract:

```proto
rpc ListCustomers(ListCustomersRequest) returns (stream Customer);
```

Both RPCs in this prototype are unary. Streaming is a genuine capability
advantage over REST, where the equivalent requires Server-Sent Events,
WebSockets, or long polling — all bolted on rather than built in.

---

## 5. Error handling: status codes

gRPC does not use HTTP status codes. It defines its own set, and every call
returns one. Common ones:

| Code | Meaning | Rough HTTP analogue |
|---|---|---|
| `OK` | success | 200 |
| `INVALID_ARGUMENT` | caller sent bad data | 400 |
| `NOT_FOUND` | entity does not exist | 404 |
| `ALREADY_EXISTS` | duplicate | 409 |
| `PERMISSION_DENIED` | authenticated but not allowed | 403 |
| `UNAUTHENTICATED` | missing/invalid credentials | 401 |
| `UNAVAILABLE` | server down or unreachable | 503 |
| `DEADLINE_EXCEEDED` | client's timeout elapsed | 504 |
| `INTERNAL` | bug on the server | 500 |
| `UNIMPLEMENTED` | method declared but not implemented | 501 |

In Go these are returned with the `status` package, as in
[`cmd/server/main.go`](cmd/server/main.go):

```go
return nil, status.Errorf(codes.NotFound, "customer %d not found", req.GetId())
```

This matters because a plain Go `errors.New` would reach the client as the
opaque code `Unknown`. Returning a real status lets the caller branch on the
*code* rather than string-matching the message:

```go
st, ok := status.FromError(err)
if ok && st.Code() == codes.NotFound {
    // handle a missing record specifically
}
```

`UNIMPLEMENTED` is worth a note. The generated
`UnimplementedCustomerServiceServer` type, embedded in our server struct,
supplies a stub returning that code for every method in the service. This is
why adding `GetCustomer` to the contract did not break the build before it was
implemented — the server simply answered `UNIMPLEMENTED` at runtime. It is
deliberate forward-compatibility, not boilerplate.

---

## 6. Key differences from REST

| | REST + JSON | gRPC |
|---|---|---|
| **Style** | resources and URLs (`GET /customers/5`) | method calls (`GetCustomer(id: 5)`) |
| **Payload** | JSON text | Protocol Buffers binary |
| **Transport** | usually HTTP/1.1 | HTTP/2 required |
| **Contract** | informal; OpenAPI/Swagger is optional and separate | `.proto` is mandatory and generates the code |
| **Type safety** | runtime only — a typo is found when it fails | compile time — a typo will not build |
| **Client code** | written by hand or generated from optional spec | always generated |
| **Streaming** | bolted on (SSE, WebSocket, polling) | built in, four call types |
| **Human-readable** | yes — `curl` and read it | no — needs `grpcurl` or similar |
| **Browser support** | native | requires gRPC-Web + a proxy |
| **Performance** | good | better: smaller payloads, multiplexed connections |
| **Error model** | HTTP status codes + ad-hoc JSON bodies | standard status codes, uniform across languages |
| **Ecosystem/tooling** | very large and mature | good, but narrower |
| **Caching** | mature HTTP caching (proxies, CDNs) | little; caching is a manual concern |

### The trade-off, stated plainly

gRPC buys **speed and a compiler-enforced contract**. It pays for this with
**human-readability, browser reach, and ecosystem breadth**.

The single most important consequence of the contract being mandatory is *when*
you find out about a mistake. With REST, renaming a JSON field and forgetting to
update one consumer produces a bug discovered in production. With gRPC, the
equivalent mistake will not compile. For a system of many small services
maintained by different people, that difference is worth a great deal.

The cost is real, though. Losing `curl` and readable payloads makes casual
debugging harder, and the toolchain (`protoc`, plugins, code generation as a
build step) is more setup than "return some JSON."

### When to use which

**Use gRPC for internal, service-to-service communication.** High call volume,
both sides under your control, and a strict contract is an asset.

**Use REST for public APIs and browser clients.** Any third-party developer can
call it with tools they already have, and no proxy is needed.

These are not mutually exclusive, and most production systems use both: a REST
(or GraphQL) API at the edge for external clients, and gRPC behind it between
internal services. This is the standard microservice pattern.

---

## 7. Application to the CRM system

A CRM naturally decomposes into several services — customers, leads,
activities, invoices, authentication, notifications. They call each other
constantly and internally: when an invoice is created, the invoice service must
confirm the customer exists; when a deal closes, several services react.

That traffic is exactly the case gRPC is designed for:

- calls are internal and frequent, so binary encoding and connection
  multiplexing pay off;
- the contracts are shared between teams, so compile-time enforcement prevents
  a whole class of integration bugs;
- `.proto` files double as the authoritative, always-current API documentation;
- future services in other languages can generate their own clients from the
  same files.

The web or mobile front end would **not** speak gRPC directly. It would call a
REST/GraphQL gateway, which then fans out to the internal services over gRPC.

The `CustomerService` in this repository is the first of those services.
`CreateCustomer` and `GetCustomer` are the two operations every other module
will depend on.

---

## 8. Summary

- gRPC is a contract-first RPC framework: define methods in a `.proto`, generate
  client and server code.
- It uses **Protocol Buffers** (compact binary, numbered fields) instead of JSON.
- It runs on **HTTP/2**, gaining multiplexing and native streaming.
- It supports **four call types**: unary, server streaming, client streaming,
  and bidirectional.
- It has its **own status codes**, not HTTP ones.
- Compared with REST it trades readability, browser support, and ecosystem
  breadth for performance and compile-time safety.
- The practical rule: **gRPC between internal services, REST at the public
  edge.**

---

## References

- [grpc.io — official documentation](https://grpc.io/docs/)
- [Go quickstart](https://grpc.io/docs/languages/go/quickstart/)
- [Protocol Buffers language guide (proto3)](https://protobuf.dev/programming-guides/proto3/)
- [gRPC status codes](https://grpc.io/docs/guides/status-codes/)
- [grpcurl](https://github.com/fullstorydev/grpcurl) — command-line client for testing

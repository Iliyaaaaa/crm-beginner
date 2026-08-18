# CRM Systems — Analysis and Microservice Design

Task 4: survey existing CRM products, extract the common modules and features,
and propose a microservice decomposition for a hypothetical CRM.

---

## 1. What a CRM actually does

A CRM (Customer Relationship Management) system is the system of record for
every interaction a company has with the people who might buy from it, or
already have.

Stripped to its core, it answers four questions:

1. **Who are our contacts and companies?** — the address book
2. **Which of them might buy, and what stage are they at?** — the pipeline
3. **What has happened with each of them?** — the history
4. **How are we performing?** — reporting

Everything else is elaboration on those four.

The distinguishing characteristic, architecturally, is that a CRM is
**relationship-centric rather than transaction-centric**. An e-commerce system
is organised around orders; a CRM is organised around a *contact* and the long
timeline of activity attached to them. Almost every entity in the system
eventually points back to a contact or an organisation.

---

## 2. Survey of existing systems

| System | Type | Notable for |
|---|---|---|
| **Salesforce** | Commercial, cloud | The reference implementation. Enormously broad; heavily customisable with its own platform, objects and permissions model. |
| **HubSpot** | Commercial, cloud | Strong marketing/sales integration. Clean, opinionated UX; free tier makes it the common starting point for small companies. |
| **Zoho CRM** | Commercial, cloud | Broad feature parity at lower cost; part of a large integrated suite. |
| **Microsoft Dynamics 365** | Commercial | Deep integration with Office/Teams; strong in enterprises already on Microsoft. |
| **Odoo** | Open source | CRM is one module of a full ERP — useful for seeing where CRM ends and ERP begins. |
| **SuiteCRM** | Open source | Fork of SugarCRM. Classic, complete, somewhat dated module structure — very readable as a feature checklist. |
| **EspoCRM** | Open source | Lighter and more modern than SuiteCRM; good reference for a minimal-but-complete data model. |
| **Twenty** | Open source, modern | Actively developed, TypeScript/GraphQL, modern architecture. **The most useful codebase to actually read** if you want to see how a CRM is built today. |

**Recommendation for study:** read **Twenty's** data model and **SuiteCRM's**
module list. Twenty shows how one is built now; SuiteCRM shows the full
traditional feature surface. Between them you have both ends of the spectrum.

---

## 3. The common modules

Nearly every system above ships the same core. This is the union, ordered
roughly by how essential each is.

### Core (present in every CRM)

| Module | Purpose | Key entities |
|---|---|---|
| **Contacts** | People | contact, phone, email, address |
| **Accounts / Companies** | Organisations, and which contacts belong to them | account, industry, size |
| **Leads** | Unqualified potential customers, before they enter the pipeline | lead, source, status |
| **Opportunities / Deals** | Qualified sales in progress | opportunity, stage, amount, close date |
| **Activities** | Calls, meetings, emails, notes, tasks — the timeline | activity, due date, assignee |
| **Users & Teams** | Who works here, who owns what | user, team, role |
| **Permissions** | Who may see and edit which records | role, permission, ownership rules |

### Common extensions

| Module | Purpose |
|---|---|
| **Pipelines** | Configurable stages; different pipelines for different sales processes |
| **Products & Price lists** | What is being sold, at what price |
| **Quotes / Orders / Invoices** | The commercial documents; boundary with ERP/billing |
| **Campaigns** | Marketing campaigns and attribution of leads to them |
| **Email integration** | Two-way sync with a mailbox; logging email against contacts |
| **Reporting & Dashboards** | Pipeline value, conversion rates, activity volume, forecasts |
| **Notifications** | Reminders, assignment alerts, digests |
| **Import / Export** | Bulk CSV in and out — universally required, universally underestimated |
| **Custom fields** | Per-tenant schema extension without code changes |
| **Audit log** | Who changed what and when |
| **Search** | Global full-text search across all entities |
| **Integrations / Webhooks** | Outbound events to other systems |

### The lifecycle that ties them together

```
   Campaign
      │  generates
      ▼
    Lead ──── qualify ────► Contact + Account
      │                          │
      │                          │ create
      ▼                          ▼
   (discard)              Opportunity ──► stage 1..n ──► Won / Lost
                               │                            │
                               │                            ▼
                               └──────► Quote ──────►  Order / Invoice

   Activities (call, email, meeting, note, task) attach to ANY of the above.
```

Understanding this flow matters more than the module list: it is the reason
Activities is the highest-traffic part of a CRM, and the reason lead→contact
conversion is a notoriously fiddly operation (it creates several records at
once and must be atomic).

---

## 4. Non-functional requirements worth naming

These shape the architecture as much as the features do.

| Requirement | Consequence |
|---|---|
| **Multi-tenancy** | Every query is scoped by tenant. Get this wrong once and one customer sees another's data — the single worst bug class in SaaS. |
| **Row-level permissions** | "Sales reps see only their own deals; managers see the team's." Cannot be bolted on afterwards. |
| **Audit trail** | Regulated industries require knowing who changed what. |
| **Soft delete** | Users delete things by accident; records usually need restoring. |
| **Custom fields** | Tenants want their own fields without a schema migration. |
| **Bulk operations** | Importing 100k contacts must not lock the system. |
| **Search latency** | Global search is used constantly and must feel instant. |
| **Data export** | GDPR-style "give me everything you hold about this person". |

**The two that most affect service boundaries** are multi-tenancy and
permissions: both cut across every service, so they belong in shared
infrastructure (middleware/interceptors), not re-implemented per service.

---

## 5. Proposed microservice decomposition

The guiding rule: **split by business capability and data ownership**, not by
technical layer. Each service owns its tables exclusively; no service reaches
into another's database.

```
                        ┌──────────────────┐
   Browser / Mobile ───►│   API Gateway    │  REST or GraphQL, public
                        │  (BFF, auth mw)  │
                        └────────┬─────────┘
                                 │  gRPC (internal only)
        ┌──────────┬─────────────┼─────────────┬──────────┐
        ▼          ▼             ▼             ▼          ▼
   ┌────────┐ ┌────────┐   ┌──────────┐  ┌──────────┐ ┌────────┐
   │Identity│ │Customer│   │  Sales   │  │ Activity │ │Reporting│
   │  &Auth │ │(contact│   │(lead/opp │  │(timeline)│ │        │
   │        │ │ account│   │ pipeline)│  │          │ │        │
   └───┬────┘ └───┬────┘   └────┬─────┘  └────┬─────┘ └───┬────┘
       │          │             │             │           │
     [db]       [db]          [db]          [db]        [read
                                                        models]
                    │             │             │
                    └─────────────┴─────────────┘
                          events (async)
                                 │
                                 ▼
                    ┌────────────────────────┐
                    │  Notification service   │
                    └────────────────────────┘
```

### Service-by-service

| Service | Owns | Why it is its own service |
|---|---|---|
| **Identity & Auth** | users, teams, roles, sessions | Different security posture; every other service depends on it; changes rarely. |
| **Customer** | contacts, accounts | The core address book. Highest read volume — nearly every other service asks it "who is customer X?" |
| **Sales** | leads, opportunities, pipelines, stages | The business logic heart. Changes most often as sales processes evolve. |
| **Activity** | calls, emails, meetings, notes, tasks | Highest **write** volume by far, and append-heavy. Isolating it keeps its load off the core entities. |
| **Reporting** | read-only projections | Analytical queries are heavy and would degrade transactional performance if run on the same database. |
| **Notification** | delivery of email/push/in-app | Pure side effect, naturally asynchronous, must not block a user request. |

### Deliberately *not* separate services (at first)

- **Products/Quotes/Invoices** — arguably belongs in billing/ERP. Keep it out of scope until the core works.
- **Search** — starts as a feature inside each service; becomes its own indexing service only when needed.
- **Custom fields** — a cross-cutting mechanism, not a service.

Resisting over-decomposition is important. Six services with clear ownership
beat fifteen with tangled dependencies.

---

## 6. Communication patterns

**Synchronous (gRPC) — when the caller needs an answer to continue.**

```
Sales.CreateOpportunity
   └─► Customer.GetCustomer(id)      "does this contact exist? what's their name?"
```

**Asynchronous (events) — when other services merely need to know it happened.**

```
Sales  ──► OpportunityWon ──►  Notification  (email the team)
                          ──►  Reporting     (update the dashboard)
                          ──►  Activity      (log a timeline entry)
```

**The rule of thumb:** if the caller must wait for the result to build its
response, use gRPC. If it is a fact others react to, publish an event. Overusing
synchronous calls creates a distributed monolith where one slow service stalls
everything; overusing events makes the system hard to reason about.

A practical starting point: **gRPC for everything, add events only where a
synchronous call is clearly wrong** (notifications, analytics, audit).

---

## 7. Data ownership

Each service has its **own database or schema**. Cross-service joins are
forbidden — you call the owning service instead.

This creates two well-known problems and their standard answers:

**"I need customer names on a list of 200 opportunities."**
Do not call `GetCustomer` 200 times. Either add a batch RPC
(`GetCustomers(ids) -> customers`), or denormalise: store a copy of the
customer's display name on the opportunity, refreshed when a
`CustomerUpdated` event arrives. Slightly stale display data is almost always
an acceptable trade.

**"I need a transaction across two services."**
You cannot have one. Restructure so the operation belongs to a single service,
or accept eventual consistency with a compensating action if a later step
fails. The usual advice — and it is good advice — is to **draw boundaries so
that this need is rare**, because the alternatives are all unpleasant.

---

## 8. Where our service fits

`crm-service` as built in Tasks 1–2 is the seed of the **Customer service**:

| Built so far | Maps to |
|---|---|
| `CustomerService.CreateCustomer` | Customer service, write path |
| `CustomerService.GetCustomer` | Customer service, read path — the RPC every other service will call |
| `customers` table in Postgres | The Customer service's private database |
| gRPC + protobuf contract | The internal service-to-service interface |

The natural next RPCs, in order of usefulness:

```proto
rpc ListCustomers(ListCustomersRequest) returns (ListCustomersResponse);  // pagination
rpc UpdateCustomer(UpdateCustomerRequest) returns (UpdateCustomerResponse);
rpc DeleteCustomer(DeleteCustomerRequest) returns (DeleteCustomerResponse); // soft delete
rpc GetCustomers(GetCustomersRequest) returns (GetCustomersResponse);      // batch, avoids N+1
```

`ListCustomers` is the most instructive one to build next: it forces you to deal
with **pagination**, which every real list endpoint needs and which is easy to
get wrong. (Prefer keyset/cursor pagination over `OFFSET` — offset degrades
badly on large tables.)

---

## 9. A sensible build order

If this hypothetical CRM were the actual project:

1. **Customer service** — contacts and accounts, full CRUD + list. ← *we are here*
2. **Identity service** — users and authentication, so requests can be attributed.
3. **API gateway** — REST/GraphQL front door, so a UI can exist at all.
4. **Sales service** — leads and opportunities; the first service that calls another.
5. **Activity service** — the timeline; introduces high write volume.
6. **Events** — introduce a broker once two services need to react to a third.
7. **Notification + Reporting** — the first genuine event consumers.

Steps 1–3 give a working, demonstrable product. Everything after adds value on
top of a spine that already functions.

---

## 10. Questions worth asking the senior

Good questions to bring to a first design discussion — they show you have
thought about the boundaries rather than just the features:

1. **Is this multi-tenant?** Single-company or SaaS with many customers? This
   changes every table and every query.
2. **Are contacts and accounts one entity or two?** Some CRMs unify them as
   "parties"; most keep them separate. It affects the whole data model.
3. **How dynamic must custom fields be?** Configurable-per-tenant is an order of
   magnitude harder than a fixed schema.
4. **Synchronous-first, or is a message broker in scope from the start?**
5. **Which service owns permissions?** Central policy service, or does each
   service enforce its own rules?
6. **Is there an existing system to migrate from?** Import requirements
   frequently dictate the data model.

---

## 11. Summary

- A CRM is organised around **contacts and their timeline**, not transactions.
- The universal core is: **Contacts, Accounts, Leads, Opportunities, Activities,
  Users, Permissions**. Everything else is an extension.
- **Multi-tenancy and row-level permissions** are cross-cutting and must be
  designed in from the start, not retrofitted.
- A reasonable decomposition is six services: **Identity, Customer, Sales,
  Activity, Reporting, Notification** — split by data ownership, each with its
  own database.
- **gRPC for synchronous internal calls, events for reactions**, REST/GraphQL
  only at the public edge where browsers connect.
- Our `crm-service` is the **Customer service**, the most-called service in the
  system — which is exactly why it was a sensible thing to build first.

---

## References

- [Twenty](https://twenty.com/) — modern open-source CRM, worth reading the source
- [SuiteCRM](https://suitecrm.com/) — traditional full feature set
- [EspoCRM](https://www.espocrm.com/) — compact, readable data model
- [Odoo CRM](https://www.odoo.com/app/crm) — CRM as part of an ERP
- [Salesforce object reference](https://developer.salesforce.com/docs/atlas.en-us.object_reference.meta/object_reference/) — the industry's de-facto vocabulary for CRM entities
- Sam Newman, *Building Microservices* — service boundaries and data ownership

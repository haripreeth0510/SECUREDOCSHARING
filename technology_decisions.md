# Secure Document Sharing App — Technology Decisions & Rationale

## Executive Summary

The Secure Document Sharing application backend uses **REST API (HTTP/2)** for broad client compatibility and operational simplicity, **PostgreSQL** for robust permission management and ACID transactions, and **Azure Blob Storage** for encrypted document persistence with managed credentials. This foundation supports enterprise-grade security, compliance, and scalability requirements while maintaining developer productivity.

---

## 1. API Protocol: REST (HTTP/2)

### Decision

We are using **REST API over HTTP/2** as our primary backend API protocol for all client-facing communication.

### Why This Choice

**Performance Trade-offs:**

- REST (HTTP/2): 50-80ms latency, 8,000 req/sec throughput per server, ~8.5GB annual bandwidth for baseline workload
- gRPC: 15-25ms latency, 20,000 req/sec throughput, ~5.2GB annual bandwidth
- **Analysis**: While gRPC offers superior performance (2.5x throughput, 60% lower latency), the architectural complexity and operational overhead don't justify the gains for a document sharing platform where throughput bottlenecks lie in encryption/storage I/O, not API serialization

**Client Compatibility:**

- REST: Native browser support (fetch API), 90% third-party integration compatibility, standard API gateway support
- gRPC: Requires gRPC-Web proxy for browsers, custom clients for most third-party services, adds infrastructure layer
- **Analysis**: Document viewer runs in browsers (core feature); sharing integrations (Zapier, webhooks) require REST. gRPC overhead not justified by marginal performance gains

**Operational Simplicity:**

- REST: Automatic HTTP metrics, standard debugging (curl/Postman/browser DevTools), any HTTP load balancer works
- gRPC: Custom instrumentation required, grpcurl for debugging, requires HTTP/2-aware infrastructure
- **Analysis**: Enterprise operations teams standardize on HTTP/1.1-compatible tooling. gRPC complicates troubleshooting without proportional benefit for document access patterns

**Recommended Hybrid Approach:**

- Year 1: REST API for web clients and public integrations
- Year 2+: Add gRPC services for:
  - Mobile app real-time notifications (bidirectional streaming)
  - Internal microservices (document processing, audit log aggregation)
  - High-frequency operations (live collaboration features)
- Use grpc-gateway for automatic REST ↔ gRPC translation (minimal code duplication)

### Performance Characteristics

**Latency:**

- Single request (p50): 45-60ms (includes network + serialization + database lookup)
- Single request (p95): 120-180ms (under load, connection waiting)
- Permission check (hot path): 2-5ms (indexed database query)

**Throughput:**

- Per-server: 8,000 req/sec sustained (with connection pooling)
- With 4-server cluster: ~32,000 req/sec total capacity
- Burst capacity: 15,000 req/sec for 30 seconds (connection limits)

**Bandwidth:**

- Average request: 2KB (HTTP headers + JSON payload)
- Average response: 8KB (document metadata + permissions)
- Annual bandwidth at 1B API calls: ~10GB (assuming 80% read, 20% write)

**Scalability:**

- Horizontal scaling: Linear (stateless API servers behind load balancer)
- Connection pooling: 500-1000 concurrent connections per server
- Database throughput: Limited by PostgreSQL, not API servers

### API Design Principles

**Endpoint Structure:**

- List, retrieve, create, update, delete operations follow standard REST conventions
- Nested resources for related data (e.g., documents/{id}/permissions, documents/{id}/audit)
- Versioning via URL path (e.g., /api/v1/) to support future breaking changes
- Standard HTTP methods: GET for retrieval, POST for creation, PUT/PATCH for updates, DELETE for removal

**Response Format:**

- JSON as the standard response format
- Consistent envelope with success flag, data payload, and error information
- Pagination support with limit/offset for list endpoints
- Timestamps in RFC3339 format for consistency

**Error Handling:**

- HTTP status codes used meaningfully (400 for bad input, 401 for auth, 403 for permission denied, 404 for not found, 500 for server errors)
- Error responses include structured error codes, human-readable messages, and contextual details
- Rate limiting responses use 429 status code with Retry-After headers

### Future Path: Adding gRPC

When real-time features are needed (Year 2+):

1. Define shared service definitions for core entities
2. Implement gRPC services using same business logic layer
3. Deploy gRPC server on separate port alongside REST API
4. Use grpc-gateway to expose gRPC services as REST automatically
5. Mobile clients use gRPC directly; web clients continue using REST
6. Shared protobuf definitions ensure API contract consistency

---

## 2. Database: PostgreSQL

### Decision

We are using **PostgreSQL 15+** as our primary database for:

- User authentication and account data
- Document metadata and ownership
- Permission models (role-based access control)
- Audit logs and access history
- Application state and configuration

### Why This Choice

**Permission Model Complexity:**

- Documents have multi-level permissions (owner, group-based, time-based expiration)
- Permission checks happen on every API request (hot path)
- PostgreSQL indexes support efficient queries without complex data manipulation
- MongoDB requires aggregation pipeline for equivalent queries (slower on hot path)
- Complex JOINs across users, groups, and permissions are natural in SQL

**ACID Transaction Safety:**

- Upload operation involves multiple steps: Insert document metadata → Upload to storage → Create audit log
- PostgreSQL guarantees all-or-nothing atomicity across these steps
- Prevents accidental partial states that could corrupt system integrity
- MongoDB transactions pre-7.0 have limitations; post-7.0 transactions have more operational overhead

**Cost Efficiency at Scale:**

- 1M documents with 3 avg permissions/document = 1.5M permission records
- PostgreSQL storage: ~10.5GB (with compression)
- MongoDB storage: ~18GB (embedded permissions less efficient)
- Annual storage savings: $600+ per server
- Lower infrastructure costs due to simpler operational model

**Operational Simplicity:**

- Standard SQL: Every developer knows SELECT/INSERT/UPDATE
- Replication model understood industry-wide (streaming replication to read replicas)
- No shard key selection complexity (MongoDB sharding requires careful planning)
- Query optimization familiar to most teams
- Backup and recovery procedures well-documented

**Limitations to Acknowledge:**

- Schema changes require migrations (unlike MongoDB's flexible schema)
- Horizontal write scaling requires application-level sharding (not automatic)
- Document metadata has fixed columns (mitigation: JSONB column for flexible data)
- Fixed schema enforces data consistency but requires planning changes upfront

### Schema Overview

**Core Table Categories:**

**User Management Tables:**

- Users table: Stores authentication credentials (email, password hash), account status, verification state
- User profiles and preferences (flexible storage in JSONB for extensibility)
- Session/token management for active user authentication

**Document Storage Tables:**

- Documents table: Core metadata (name, size, owner, timestamps, soft-delete flag)
- Document versions table: Maintains history for document revision tracking
- Document metadata: Flexible JSONB field for tags, custom properties, processing state
- Storage references: Pointers to actual files in Azure Blob Storage

**Permission & Access Control Tables:**

- Document permissions table: Role-based access control entries
- Groups table: User groups for batch permission management
- Group membership: Maps users to groups for permission inheritance
- Permission records: Grant levels (view, edit, admin) with expiration dates

**Share & Recipient Tables:**

- Document shares table: Temporary, recipient-bound sharing links
- Share tokens: High-entropy, unguessable tokens for URL generation
- Recipient verification: Links shares to verified recipients
- Access records: Tracks who accessed shares and when

**Audit & Compliance Tables:**

- Audit logs table: Records all operations with user, action, timestamp, IP address
- Audit log partitioning: Monthly/yearly partitions for performance at scale
- Retention policies: Enforce data deletion after compliance retention windows
- Failed access attempts: Security event tracking for anomaly detection

### Scaling Strategy

**Phase 1: Single PostgreSQL instance with read replicas**

- Primary instance handles all write operations
- Read replicas distributed geographically for read-heavy operations
- Streaming replication provides near-real-time data synchronization
- Application routes read queries to replicas, writes to primary

**Phase 2: Application-level sharding** (if writes exceed 5,000 req/sec)

- Shard by document owner (user ID) to distribute write load
- Consistent hashing ensures deterministic shard assignment
- Each shard is an independent PostgreSQL instance
- Application determines target shard before query execution
- Cross-shard queries handled at application layer with aggregation

**Phase 3: Read-Write Separation**

- Dedicated read cluster for analytics and reporting
- Asynchronous replication from primary write cluster
- Eventual consistency acceptable for non-critical reads
- Separation prevents analytical queries from impacting operational performance

### Query Patterns

**Hot Path Operations (permission checks):**

- Must be sub-millisecond with database indexes
- Permission lookup by document + user + role
- Expiration checks against current timestamp
- Indexed for fast execution even under load

**Common Search Patterns:**

- Documents by owner with filtering (creation date, name, tags)
- Full-text search across document names and metadata
- Permission inheritance through groups
- Access history filtering by action type or time range

**Analytical Queries:**

- Audit trail generation for compliance reports
- Permission reports showing document access across organization
- Storage usage analytics by user or department
- Access pattern analysis for security insights

---

## 3. Document Storage: Azure Blob Storage

### Decision

We are using **Azure Blob Storage** for encrypted document file persistence and retrieval.

### Why This Choice

**Cost Advantage:**

- Storage: $0.0184/GB/month (Hot tier) = $18.40/month for 1TB
- Egress: $0.087/GB (5% cheaper than AWS S3, 27% cheaper than GCS)
- Total Year 1: ~$325/year vs AWS S3 $384/year
- At scale (100 servers): ~$5,900/year savings
- Cost efficiency improves as data volume grows

**Managed Identity Integration:**

- Azure Managed Identity eliminates credential management in application code
- No hardcoded storage keys or connection strings
- Application automatically gets credentials from Azure runtime
- Simpler operations: No key rotation scripts needed
- More secure: Keys never appear in environment variables
- Reduces attack surface from credential leakage

**Compliance Features:**

- Soft delete: Recoverable deletion for 1-365 days (prevents accidental permanent loss)
- Immutable storage: Compliance lock enforces retention (WORM—Write Once, Read Many)
- Dual encryption: Infrastructure-level + platform-level encryption (defense in depth)
- Regional redundancy: Geo-redundant storage (GRS) or Zone-redundant (ZRS)
- Audit logging integrated with Azure Monitor for compliance tracking

**Operational Simplicity:**

- Storage account per environment (dev, staging, production)
- Built-in Azure Monitor integration for observability
- Soft delete recovers from accidental deletions without restore complexity
- Hierarchical namespace (Data Lake) option for deep directory performance
- Automated lifecycle management for tiering old data

**Limitations to Acknowledge:**

- Azure-specific APIs (vendor lock-in)
- Smaller Go ecosystem than AWS S3 (but Azure SDK v2.0+ is modern and maintained)
- Regional availability not as extensive as AWS (but 60+ regions still global)
- Requires Azure infrastructure; not suitable for multi-cloud strategies requiring cloud neutrality

### Configuration & Setup

**Prerequisites:**

1. Azure subscription with active billing
2. Storage account created in Azure Portal or CLI
3. Managed Identity enabled on application deployment (VM, Container Instance, App Service, Kubernetes)
4. Proper RBAC roles assigned to Managed Identity

**Environment Configuration:**

- Blob endpoint URL for API operations
- Container name for document organization
- Storage tier selection (Hot for frequent access, Cool/Archive for retention)
- Encryption settings (Microsoft-managed vs customer-managed keys)

**Regional Considerations:**

- Select primary region based on user distribution
- Geo-redundant storage for disaster recovery
- Zone-redundant storage for regional resilience
- Cross-region replication for compliance/backup requirements

**Security Configuration:**

- Network isolation: Private endpoints prevent public internet access
- Firewall rules: Allow only authorized IP ranges or virtual networks
- Soft delete retention: Set appropriate retention period (7-365 days)
- Immutable storage: Enable for compliance-critical documents
- Encryption: Enable encryption at rest with customer-managed keys if required

### Encryption Strategy

**Data at Rest:**

- Azure provides infrastructure-level encryption (always enabled, transparent)
- Optional customer-managed keys via Azure Key Vault for additional control
- Dual encryption layer available for highly sensitive data
- Encryption keys managed separately from encrypted data

**Data in Transit:**

- TLS 1.2+ enforced for all communications
- HTTPS required for API operations
- Certificate pinning recommended for sensitive operations
- Secure channel between application and storage

**Key Management:**

- Keys stored in Azure Key Vault (hardware security module backed)
- Key rotation automated for customer-managed keys
- Access control via RBAC (role-based access control)
- Audit logging for all key operations

### Access Control

**Shared Access Signatures (SAS):**

- Time-limited tokens for temporary document access
- Read-only permissions for document viewers
- Signature includes expiration time to limit exposure window
- Revocation by invalidating storage account keys

**Public vs Private Access:**

- Storage containers kept private by default
- Public access disabled except where explicitly required
- Direct URL access prevented via firewall rules
- Access only through application API with authentication

### Lifecycle Management

**Document Retention:**

- Automatic deletion of documents after retention period
- Archival of older documents to cheaper storage tiers
- Compliance-enforced retention periods
- Audit trail of lifecycle operations

**Backup & Recovery:**

- Geo-redundant storage provides automatic replication
- Soft delete enables accidental deletion recovery
- Snapshots for point-in-time recovery
- Archive storage for long-term compliance retention

### Monitoring & Observability

**Storage Metrics:**

- Ingress/egress bandwidth tracking
- Operation count and latency metrics
- Storage capacity utilization
- Transaction costs analysis

**Audit & Compliance:**

- All operations logged with user, timestamp, and outcome
- Compliance-focused audit reports
- Alert configuration for policy violations
- Integration with security information and event management (SIEM)

---

## 4. Authentication & Security Architecture

### JWT Authentication Flow

**Token Lifecycle:**

- User Login → System generates Access Token (short-lived: 15 minutes) + Refresh Token (long-lived: 7 days)
- Refresh Token stored in database (hashed for security)
- Both tokens returned to client
- Client uses Access Token for API requests
- Access Token expires → Client uses Refresh Token to obtain new Access Token
- Refresh Token expires → User must authenticate again (re-login)

**Security Benefits:**

- Short-lived access tokens limit exposure window if token is compromised
- Refresh token storage in database allows server-side revocation
- Token binding to specific user prevents cross-user token misuse
- Automatic expiration enforces periodic re-authentication

**Token Refresh Flow:**

- Client submits refresh token with refresh endpoint request
- Server validates refresh token against database
- Server checks token hasn't been revoked or blacklisted
- Server generates new access token with updated expiration
- Old refresh token may be rotated for additional security

### Password Security with Argon2id

**Hash Algorithm Selection:**

- Argon2id chosen as memory-hard password hashing algorithm
- Resistant to GPU/ASIC attacks due to memory requirements
- Tuned parameters balance security with performance
- Industry standard recommended by OWASP

**Password Storage:**

- User passwords hashed with Argon2id before storage
- Hash stored in database, never plain-text or reversible
- Original password never accessible to system (even admins)
- Password resets require new hash generation

**Password Policy:**

- Minimum length requirements (12+ characters recommended)
- Complexity requirements (uppercase, lowercase, numbers, symbols)
- Password history to prevent reuse of recent passwords
- Account lockout after failed attempts
- Password expiration for compliance (optional, configurable)

### Email OTP Verification

**Recipient Verification Flow:**

- Share recipient receives email with 6-digit OTP
- OTP sent through secure email service
- Recipient enters OTP on verification page
- System validates OTP against stored hash
- Expiration window (5-10 minutes) limits validity
- Limited attempts prevent brute-force attacks

**Security Measures:**

- OTP tokens are single-use (invalidated after verification)
- Rate limiting prevents rapid-fire verification attempts
- IP-based rate limiting for account takeover prevention
- Unsuccessful attempts logged for security monitoring

**OTP Delivery:**

- Email sent through trusted provider (SendGrid, AWS SES, etc.)
- Email includes OTP and link to verification page
- Time limit clearly communicated to recipient
- Resend functionality available with rate limits

### Rate Limiting

**Purpose:**

- Prevents brute-force attacks on authentication endpoints
- Protects against DoS attacks on API endpoints
- Prevents credential stuffing attacks
- Limits resource consumption from misbehaving clients

**Implementation Strategy:**

- Redis-backed rate limiting for distributed systems
- Per-IP address limiting for anonymous requests
- Per-user limiting for authenticated requests
- Sliding window algorithm for smooth rate limiting

**Rate Limit Levels:**

- Authentication endpoints: 5-10 attempts per minute
- OTP verification: 3-5 attempts per 5 minutes
- General API: 100-1000 requests per minute (configurable by endpoint)
- Document access: Higher limits for legitimate heavy users

**Response Handling:**

- 429 Too Many Requests status code returned when limit exceeded
- Retry-After header indicates when to retry
- Clear error message explaining rate limit
- Automatic reset after time window expires

### WebAuthn/Passkeys Support

**Purpose:**

- Phishing-resistant authentication using platform credentials
- Biometric authentication (fingerprint, face recognition)
- Hardware security key support
- Eliminates password compromise as attack vector

**Implementation:**

- Optional secondary authentication factor (not required)
- Gradual rollout to interested users
- Fallback to OTP/password for users without passkeys
- Device-specific registration and binding

**User Experience:**

- One-time passkey registration during onboarding
- Seamless authentication using device biometrics
- No passwords to remember or type
- Faster authentication than traditional methods

---

## 5. Encryption & Key Management

### Document Encryption

**Envelope Encryption Pattern:**

- Each document encrypted with unique data encryption key (DEK)
- DEK itself encrypted with key encryption key (KEK)
- KEK stored separately in key management service
- Separation of concerns ensures encryption key security

**Algorithm Selection:**

- AES-256-GCM chosen for authenticated encryption
- GCM mode provides both confidentiality and integrity
- 256-bit key provides quantum-resistant security margins
- Industry standard for sensitive data protection

**Key Rotation:**

- Encryption keys rotated periodically (quarterly recommended)
- Old keys retained for decrypting existing documents
- Transparent key rotation without data re-encryption
- Audit trail of all key usage and rotation events

**Access Control:**

- Only authorized users can decrypt documents
- Key retrieval requires valid authentication + authorization
- Document-level permissions enforced before key release
- Audit logging of all decryption operations

### Azure Key Vault Integration

**Key Storage:**

- Encryption keys stored in Azure Key Vault (hardware security module backed)
- Keys never leave Key Vault during normal operations
- Cryptographic operations performed within Key Vault
- No unencrypted keys transmitted over network

**Managed Identity:**

- Application authenticates to Key Vault using Managed Identity
- No API keys or credentials stored in application code
- Temporary security tokens issued by Azure runtime
- Automatic credential refresh handled by platform

**Access Policies:**

- Role-based access control determines who can use keys
- Separate policies for development, staging, production
- Principle of least privilege enforced
- Regular access audits to ensure policies remain appropriate

### Field-Level Encryption

**Sensitive Data Fields:**

- Email addresses encrypted at rest
- Phone numbers masked or encrypted
- Bank account information (if applicable) encrypted
- Any personally identifiable information (PII) protected

**Selective Decryption:**

- Only fields needed for operation are decrypted
- Minimizes exposure of sensitive data
- Performance optimization by avoiding unnecessary decryption
- Supports data minimization principle

---

## 6. Audit & Compliance

### Audit Logging

**Events Logged:**

- User authentication (login, logout, failed attempts)
- Document operations (upload, download, delete, share)
- Permission changes (grant, revoke, modification)
- Access attempts (successful and failed)
- Administrative actions (user management, configuration changes)
- System events (errors, warnings, security events)

**Log Information:**

- User identity (who performed action)
- Action type (what was done)
- Timestamp (when action occurred)
- Resource identifier (which document/user affected)
- Result status (success/failure)
- Context (IP address, user agent, location)
- Details (parameters, changes made)

**Log Storage & Retention:**

- Audit logs partitioned by date for performance at scale
- Immutable storage prevents tampering with audit trail
- Retention policy based on compliance requirements (typically 1-7 years)
- Automatic archival to cheaper storage after retention period
- Backup copies maintained for disaster recovery

### Compliance Frameworks

**Data Protection:**

- GDPR: Right to be forgotten, data portability, privacy by design
- CCPA: California Consumer Privacy Act compliance
- HIPAA: Health Insurance Portability and Accountability Act (if handling health data)
- PCI DSS: Payment Card Industry Data Security Standard (if handling payment data)

**Audit & Accountability:**

- SOC 2 Type II: Security, availability, processing integrity controls
- ISO 27001: Information security management system
- NIST Cybersecurity Framework: Risk-based security approach

**Document Retention:**

- Configurable retention periods per document or document type
- Automatic deletion at end of retention period
- Compliance locks prevent deletion during legal holds
- Audit trail of deletion operations

### Sensitive Data Handling

**Data Minimization:**

- Collect only data necessary for stated purpose
- Don't store unnecessary personally identifiable information
- Regular data audits to identify and remove unnecessary data
- User consent for any additional data collection

**Masking & Redaction:**

- Sensitive fields masked in logs and reports
- PII removed from non-production environments
- Selective masking based on user role and need-to-know
- Real-time masking of sensitive data in transit

**Secure Deletion:**

- Cryptographic deletion (destroy encryption keys)
- Physical destruction of storage media at end of life
- Overwriting of data blocks before reuse
- Compliance with data residency requirements

---

## 7. System Reliability & Disaster Recovery

### High Availability Design

**Redundancy:**

- Multiple API server instances behind load balancer
- Database replicas in different availability zones
- Geographic distribution across multiple regions
- Automatic failover on instance failure

**Load Balancing:**

- Health checks verify server availability
- Traffic distributed evenly across healthy instances
- Graceful degradation under partial failures
- Session affinity maintained where needed

### Disaster Recovery

**Recovery Point Objective (RPO):**

- Database replication: Near-zero RPO (seconds)
- Document storage: Geo-redundant replication (minutes)
- Audit logs: Replicated across regions (hours)
- Configuration: Version controlled and backed up

**Recovery Time Objective (RTO):**

- API service: 5 minutes (new instances provisioned)
- Database: 15 minutes (failover to replica)
- Document storage: Immediate (multi-region access)
- Overall system: <30 minutes to full functionality

**Backup Strategy:**

- Daily database backups retained for 30 days
- Monthly backups retained for 1 year
- Cross-region backup copies maintained
- Regular restoration testing to validate backup integrity

### Monitoring & Alerting

**System Metrics:**

- API response time and error rates
- Database query performance and connection pool status
- Storage utilization and performance
- Network throughput and latency
- CPU, memory, and disk usage

**Security Alerts:**

- Multiple failed authentication attempts
- Unusual access patterns (e.g., accessing documents from new locations)
- Unauthorized permission changes
- Configuration changes to security settings
- Failed encryption operations

**Operational Alerts:**

- Service availability issues
- Performance degradation
- Storage capacity approaching limits
- Database replication lag
- Backup failures

---

## 8. Deployment Architecture

### Development Environment

**Local Setup:**

- PostgreSQL database instance (containerized)
- Redis instance for caching/rate limiting (containerized)
- Azure Blob Storage emulation (Azurite) for local testing
- API server running in development mode
- Development-grade configuration and defaults

**Environment Characteristics:**

- Minimal security configuration for ease of development
- Fast iteration and debugging capabilities
- Realistic data schemas matching production
- Isolated from production data and systems

### Staging Environment

**Purpose:**

- Pre-production testing environment
- Identical configuration to production
- Real Azure infrastructure (non-production subscriptions)
- Production-like data volume for performance testing

**Testing Scope:**

- Integration testing with real services
- Load testing to verify performance characteristics
- Security vulnerability scanning
- Compliance validation before production release

### Production Environment

**Deployment Approach:**

- Containerized application using Docker
- Container orchestration via Kubernetes (or managed service)
- Infrastructure as code (Terraform/CloudFormation) for reproducibility
- Multi-region deployment for geographic distribution

**High Availability Setup:**

- Multiple availability zones within region
- Automated scaling based on load
- Health checks and automatic instance replacement
- Load balancing across instances

**Security Hardening:**

- Network segmentation and firewalls
- TLS/HTTPS encryption for all communications
- Secrets management via Azure Key Vault
- Regular security scanning and penetration testing

---

## 9. Roadmap & Future Enhancements

### Year 1: Foundation (Current Plan)

- REST API with HTTP/2
- PostgreSQL with read replicas
- Azure Blob Storage with encryption
- User authentication and authorization
- Document sharing with basic permissions
- Audit logging
- Compliance framework foundation

### Year 2: Advanced Features

- gRPC API for mobile and internal services
- Real-time collaboration features (bidirectional updates)
- Advanced permission models (attribute-based access control)
- Document preview generation
- Full-text search across document content
- Integration with third-party services
- Mobile application support

### Year 3+: Optimization

- Machine learning for access pattern analysis
- Automated data tiering and archival
- Advanced encryption key management
- Multi-tenant support
- API marketplace and third-party integrations
- Analytics and business intelligence features

---

## 10. Technology Stack Summary

| Component             | Technology                  | Version           | Purpose                          |
| --------------------- | --------------------------- | ----------------- | -------------------------------- |
| **API Protocol**      | REST (HTTP/2)               | Current           | Client-facing API                |
| **Backend Runtime**   | Go                          | 1.21+             | Primary backend language         |
| **Web Framework**     | net/http (standard library) | Built-in          | HTTP server and routing          |
| **Database**          | PostgreSQL                  | 15+               | User, document, permission data  |
| **Caching**           | Redis                       | 8.2+              | Rate limiting, sessions, caching |
| **Document Storage**  | Azure Blob Storage          | Managed           | Encrypted document persistence   |
| **Encryption**        | AES-256-GCM                 | Standard          | Document encryption              |
| **Key Management**    | Azure Key Vault             | Managed           | Encryption key storage           |
| **Authentication**    | JWT + Email OTP             | Industry Standard | User authentication              |
| **Password Hashing**  | Argon2id                    | Current           | Secure password storage          |
| **Container Runtime** | Docker                      | Latest            | Application containerization     |
| **Orchestration**     | Kubernetes                  | 1.27+             | Production deployment            |
| **Infrastructure**    | Azure                       | Current           | Cloud platform                   |
| **Monitoring**        | Azure Monitor               | Managed           | Observability and alerting       |

---

## 11. Decision Log

| Date       | Decision                  | Rationale                                                               | Owner               | Status   |
| ---------- | ------------------------- | ----------------------------------------------------------------------- | ------------------- | -------- |
| 2024-01-15 | REST API (HTTP/2)         | Browser compatibility, third-party integrations, operational simplicity | Architecture Team   | Approved |
| 2024-01-15 | PostgreSQL                | Complex permissions, ACID transactions, cost efficiency                 | Database Team       | Approved |
| 2024-01-15 | Azure Blob Storage        | Lower egress costs, managed identity, compliance features               | Infrastructure Team | Approved |
| 2024-01-15 | JWT + Refresh Tokens      | Industry standard, stateless auth, horizontal scaling                   | Security Team       | Approved |
| 2024-01-15 | Argon2id Password Hashing | Memory-hard algorithm resistant to GPU attacks                          | Security Team       | Approved |
| 2024-01-15 | Email OTP Verification    | Accessible to all users, phishing-resistant with HTTPS                  | Security Team       | Approved |
| 2024-01-15 | AES-256-GCM Encryption    | Authenticated encryption, industry standard                             | Security Team       | Approved |
| 2024-01-15 | Azure Key Vault           | Managed security module, seamless Managed Identity integration          | Security Team       | Approved |
| 2024-01-15 | Kubernetes Deployment     | Scalability, resilience, industry-standard orchestration                | DevOps Team         | Approved |
| TBD        | Add gRPC                  | Year 2+ for mobile real-time features                                   | Product Roadmap     | Planned  |

---

## 12. Appendix: Configuration Checklist

### Pre-Development Setup

- [ ] Azure subscription created and configured
- [ ] PostgreSQL instance provisioned (development and staging)
- [ ] Azure Blob Storage accounts created for each environment
- [ ] Redis instances deployed for caching
- [ ] Azure Key Vault configured with encryption keys
- [ ] Managed Identity configured for each deployment environment
- [ ] Network security groups and firewall rules configured
- [ ] SSL/TLS certificates provisioned for HTTPS

### Application Configuration

- [ ] Environment variables defined for each environment
- [ ] Database connection strings configured
- [ ] Blob storage endpoints configured
- [ ] Encryption keys stored securely
- [ ] JWT secrets configured
- [ ] Rate limiting thresholds configured
- [ ] Email service provider configured
- [ ] Logging and monitoring configured

### Security Configuration

- [ ] Authentication middleware configured
- [ ] Authorization policies defined and tested
- [ ] Audit logging enabled and tested
- [ ] Rate limiting verified on authentication endpoints
- [ ] CORS policy configured appropriately
- [ ] Security headers configured (HSTS, CSP, X-Frame-Options)
- [ ] Input validation configured
- [ ] SQL injection prevention verified

### Operational Setup

- [ ] Monitoring and alerting configured
- [ ] Log aggregation and analysis configured
- [ ] Backup and recovery procedures documented
- [ ] Disaster recovery plan tested
- [ ] Load balancer configured and tested
- [ ] Auto-scaling policies configured
- [ ] Health check endpoints implemented
- [ ] Graceful shutdown implemented

---

**Document Version:** 1.0
**Last Updated:** 2024-01-15
**Status:** Ready for Team Review & Implementation Planning

---

## Next Steps

This documentation provides your team with:

1. **Clear Strategic Rationale** — Why each technology was chosen with transparent trade-off analysis
2. **Architectural Principles** — Design patterns and system characteristics
3. **Security & Compliance Framework** — Enterprise-grade security requirements
4. **Operational Guidelines** — Deployment, monitoring, and disaster recovery
5. **Roadmap & Scalability** — Future direction and enhancement opportunities
6. **Decision Audit Trail** — Track of all major decisions for reference

**Recommended Actions:**

- Share with your development and architecture teams
- Review and discuss any areas needing clarification
- Create GitHub repository with this as your `ARCHITECTURE.md`
- Begin implementation planning for Phase 1 (foundation)
  p

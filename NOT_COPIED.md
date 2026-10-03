# Not Copied Manifest

This document records important TokenTimer components that were deliberately NOT copied into the WhatBreaks MVP workspace, and the reasoning behind their exclusion.

## Excluded Components

| Component | Reason for Exclusion from WhatBreaks MVP |
|-----------|------------------------------------------|
| **AWS, GCP, Azure, Vault Integrations** | WhatBreaks MVP is strictly Kubernetes-only. Other integrations will be built later. |
| **CertOps Logic** | Certificate lifecycle management is specific to TokenTimer and irrelevant to WhatBreaks dependency analysis. |
| **Certificate / Expiration Workers** | Expiration and alert workers are specific to TokenTimer's certificate expiration alerts. |
| **TokenTimer Dashboard / UI** | WhatBreaks will require an entirely different UI focused on dependency graphs and impact analysis. |
| **TokenTimer Business Routes** (`/certificates`, `/alerts`, etc.) | Highly coupled to TokenTimer's specific data models and use cases. |
| **Cert-Manager Observer Logic** | WhatBreaks will need a new observer capable of watching standard K8s resources (Pods, Deployments, etc.), rather than CertManager certificates. |
| **Secret / Key Extraction Logic** | **CRITICAL SECURITY REQUIREMENT**: WhatBreaks must never extract or persist secret values (`data`, `stringData`, or private keys). It only requires metadata. TokenTimer's secret extraction logic was excluded to guarantee this. |
| **Database Models / Migrations** | WhatBreaks will have its own distinct models for nodes, edges, and evidence. |

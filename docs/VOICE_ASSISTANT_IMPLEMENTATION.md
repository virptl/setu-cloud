# Setu Cloud Voice Assistant Support: Implementation & Future Scope Report

This document provides a detailed technical report on the implementation of **Voice Assistant Support** (Amazon Alexa & Google Assistant) inside the Setu Cloud platform, including key architectural components, bug fixes resolved during testing, and identified system gaps with future optimization scopes.

---

## 1. Executive Summary

Setu Cloud now supports schema-driven voice assistant integrations. Similar to the Tuya IoT model, products can explicitly declare whether they support Amazon Alexa and/or Google Assistant at the schema definition level. 

During account linking and discovery:
- Users authorize their accounts using OAuth2.
- Only devices belonging to products with voice assistant support enabled are exposed to the voice platforms.
- Unconfigured or disabled devices are strictly hidden from discovery, state queries, commands, and proactive background updates.

---

## 2. Core Implementation Details

### A. Schema & Profile Projections
- **File**: [schema.go](file:///root/viral/setu-cloud/internal/schema/schema.go)
  - Defined the `AssistantConfig` struct mapping `enabled`, `alexa`, and `google` flags.
  - Extended the `Artifact` and `panel` parsing logic to unmarshal and validate this block.
  - Projects these rules into the compiled `schema.Profile` object.
- **File**: [profiles.go](file:///root/viral/setu-cloud/internal/app/profiles.go)
  - Extended the hardcoded static `productProfiles` catalog (e.g., enabling support for `light-rgbcw`, `light1`, `th1`, `sp1` and disabling it for `gen1`).
  - Implemented `ResolveAssistantConfig(ctx, db, pid)` to extract these configurations dynamically (querying database schema definitions first, then falling back to catalog defaults).
  - Implemented `IsAssistantSupported(ctx, db, pid, platform)` for querying platform-specific authorization (`"alexa"`, `"google"`, or `"any"`).

### B. IoT Service Layer Integration
- **File**: [service.go](file:///root/viral/setu-cloud/internal/iot/service.go)
  - Added `ListDevicesForAssistant(ctx, uid, platform)` to fetch and filter user-owned devices against their assistant schema permissions.
  - Added `OwnsDeviceForAssistant(ctx, uid, did, platform)` to validate device ownership and assistant platform compatibility in a single step.

### C. Voice Platform Handlers
- **File**: [alexa/handler.go](file:///root/viral/setu-cloud/internal/alexa/handler.go)
  - Integrated `ListDevicesForAssistant` into `handleDiscovery` (`Alexa.Discovery`).
  - Integrated `OwnsDeviceForAssistant` into `handleReportState` and `handleControl`.
- **File**: [google/handler.go](file:///root/viral/setu-cloud/internal/google/handler.go)
  - Integrated `ListDevicesForAssistant` into `handleSync` (`action.devices.SYNC`).
  - Integrated `OwnsDeviceForAssistant` into `handleQuery` and `handleExecute`.

### D. Proactive Events Filter
- **File**: [proactive/service.go](file:///root/viral/setu-cloud/internal/proactive/service.go)
  - Extended the background event publisher to check `OwnsDeviceForAssistant` before pushing `ChangeReport` (Alexa) or `ReportState` (Google HomeGraph) notifications, reducing overhead and preventing leaks of non-assistant device states.

### E. Critical OAuth Bug Fix Resolved
- **File**: [store.go](file:///root/viral/setu-cloud/internal/oauth/store.go)
  - **Issue**: During token exchange (`POST /oauth/token`), the SQL query was looking for the column `id` on the `oauth_auth_codes` table. Because `oauth_auth_codes` uses `code` as the primary key and lacks an `id` column, the database query failed internally, returning a `400 Bad Request (invalid code)` error and displaying a blank screen during mobile account linking.
  - **Fix**: Corrected the SQL select and update statements to query and update based on the `code` column, enabling clean OAuth code exchanges.

---

## 3. Operational Setup

To configure the integration, developers set up one-time console rules and register clients.

### Webhook & OAuth Routing
- **Subdomain Webhook**: `https://api.setuiot.com/google/smarthome` (and `/alexa/smarthome`)
- **OAuth Authorization**: `https://api.setuiot.com/oauth/authorize`
- **OAuth Token Exchange**: `https://api.setuiot.com/oauth/token`

### Automation Script
We added [refresh_google_token.py](file:///root/viral/setu-cloud/scripts/refresh_google_token.py) to securely retrieve a short-lived bearer token using your Google Service Account JSON key and dynamically inject the `GOOGLE_SA_TOKEN` environment variable into your `.env` file hourly.

---

## 4. Google Developer Console Configuration Summary

The Google Smart Home integration was set up via the Google Actions Console using the following specific parameters:

1. **Smart Home Project Creation**: Initialized a new smart home project named `Setu Smart Home`.
2. **Fulfillment URL**: Configured the cloud endpoint webhook to `https://api.setuiot.com/google/smarthome`.
3. **OAuth2 Account Linking**:
   - **Client ID**: `google-smart-home` (registered in the Postgres `oauth_clients` table).
   - **Client Secret**: Configured to match the hashed credentials in the Setu database.
   - **Authorization URI**: `https://api.setuiot.com/oauth/authorize`
   - **Token URI**: `https://api.setuiot.com/oauth/token`
   - **Scopes**: Added `devices:read` and `devices:control`.
4. **App Directory Asset Setup**: Uploaded the official Setu brand mark assets generated at `144x144px` (App Icon) and `192x192px` (Company Logo) to comply with Google developer console visual guidelines.
5. **GCP Console Integration**:
   - Enabled **HomeGraph API** for the Google Cloud project.
   - Created a service account `setu-proactive-reporter` with the **Homegraph API Service Agent** role and downloaded the private key JSON file.

---

## 5. Certification & Public Listing Steps (Works with Google)

Currently, the Setu Smart Home integration is in **testing mode**. This means only developer accounts added to the project can see and link `[test] Setu Smart Home` in their Google Home App. 

For the **Setu Smart Home** app to appear publicly for all end-users in the standard **"Works with Google"** list, you must complete the Google Smart Home certification process:

### Step 1: Complete Directory Information
Fill in all brand details under the **Deploy -> Directory Information** tab in the Google Actions Console:
- Short and long descriptions of the Setu Smart Home integration.
- Company name, developer email, and contact details.
- Developer Logo (`192x192px`) and App Icon (`144x144px`).
- **Privacy Policy URL**: Must point to `https://api.setuiot.com/privacy` (which we set up in our backend).

### Step 2: Self-Certification Testing (Test Suite)
Run the automated **Smart Home Test Suite** to verify your fulfillment webhook behaves correctly:
1. Open the [Google Home Test Suite Tool](https://smarthome-test-suite.appspot.com/).
2. Log in with your Actions Console developer account and select your project.
3. Authenticate with a test Setu Cloud user account containing at least one claimed online device.
4. Execute the automated tests to validate `SYNC`, `QUERY`, and `EXECUTE` commands for all supported device types. All tests must pass.

### Step 3: Brand & Domain Verification
1. Verify ownership of your API domain (`api.setuiot.com` / `setuiot.com`) inside your Google Search Console.
2. Under the Google Actions Console, associate the verified domain with your Action.
3. If linking directly from your Android mobile app (App Flip), specify your app's package name and upload your `assetlinks.json` file to your domain's `.well-known` path.

### Step 4: Submit for Certification Review
1. Go to **Deploy -> Release** in the Google Actions Console.
2. Click **Submit for review**.
3. Google's engineering team will review the webhook performance, test account flow, and brand assets. This process typically takes 3 to 7 business days.
4. Once approved, the integration goes live, and `Setu Smart Home` will appear globally in the **Works with Google** list.

---

## 6. Identified Gaps & Technical Debt

1. **Schema Propagation Delay**: When a product's assistant configuration is updated in the Developer Portal, the changes only take effect for devices once the user forces a new sync (e.g., unlinking/linking the account or asking *"sync my devices"*). 
2. **Missing Token Expiry Safeguards**: If the automated service account token refresh script fails, proactive reporting will fail silently without raising alerts.
3. **Implicit Capabilities Mapping**: The current implementation maps Setu Cloud DPs directly to voice assistant capabilities using hardcoded rules inside the adapters (e.g., DP 1 always maps to `OnOff`). It does not support custom, user-defined, or dynamic DP-to-intent mappings.

---

## 7. Future Scope & Roadmap

### Phase 1: Developer Portal UI Integration
- **Dynamic Configuration UI**: Introduce toggle switches inside the Developer Portal's product interaction setup screen allowing developers to enable/disable Alexa/Google Assistant and customize device traits directly.
- **DP-to-Intent Mapper**: Provide a visual mapper interface where developers can bind custom Data Points to specific voice controller interfaces (e.g., mapping DP 15 to a custom RangeController for fan speed).

### Phase 2: Enhanced Proactive Notifications
- **RequestSync Automation**: Automate the triggers for `requestSync` calls from Setu Cloud when a new device is claimed, renamed, or deleted so changes populate instantly on the Google Home App without manual user intervention.
- **Webhook Alerts**: Set up system alerts/logs if Google HomeGraph API calls start returning HTTP errors (e.g., due to expired service account credentials).

### Phase 3: Matter & Local Execution
- **Matter Bridge Capability**: Research bridging Setu Cloud MQTT devices into a local Matter fabric via a bridge gateway, enabling high-performance local control (low latency, offline execution) directly through Google Nest Hubs and Amazon Echos.
- **Google Local Home SDK**: Implement the Google Local Home SDK to allow Google Home speakers to send control commands locally via UDP/local-key encryption instead of routing through the cloud.

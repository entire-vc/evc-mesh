# Extensions API

- `GET/PUT /api/v1/extensions/{id}/instance-settings`: human instance administrator only; secrets are masked on GET, a masked value on PUT keeps the stored secret.

Instance administrator = a human user (never an agent key) with the owner/admin role in the oldest non-deleted workspace of the instance. There is no separate instance-level role.

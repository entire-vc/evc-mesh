-- Synthetic catalog extensions for the Connect e2e: installed, allowed and
-- configured at instance level only. No workspace connection exists.
-- keep: own address key and a workspace settings block; keep-rotator: another
-- address key and no workspace fields; keep-refused: keep shape whose instance
-- address is not a valid service address (the server must refuse the connect).
INSERT INTO installed_extensions (extension_id, version, manifest, source, trust_level) VALUES
 ('keep', '0.6.5', '{"schema":1,"id":"keep","name":"Keep","version":"0.6.5","kind":"plugin","runtime":"external","mesh_api":">=1","publisher":"example","category":"development","levels":{"instance":true,"workspace":true,"project":false},"capabilities":{"api":["tasks:read"],"events":[],"ui":[]},"settings":{"workspace":{"type":"object","properties":{"own_credentials":{"type":"boolean","title":"Own credentials"}}}},"external":{"base_url_setting":"keep_url","health_path":"/healthz","install_path":"/mesh/install","events_path":"/mesh/events"}}', 'catalog', 'custom'),
 ('keep-rotator', '0.1.0', '{"schema":1,"id":"keep-rotator","name":"Keep rotator","version":"0.1.0","kind":"plugin","runtime":"external","mesh_api":">=1","publisher":"example","category":"development","levels":{"instance":true,"workspace":true,"project":false},"capabilities":{"api":["tasks:read"],"events":[],"ui":[]},"external":{"base_url_setting":"rotator_url","health_path":"/healthz","install_path":"/mesh/install","events_path":"/mesh/events"}}', 'catalog', 'custom'),
 ('keep-refused', '0.1.0', '{"schema":1,"id":"keep-refused","name":"Keep refused","version":"0.1.0","kind":"plugin","runtime":"external","mesh_api":">=1","publisher":"example","category":"development","levels":{"instance":true,"workspace":true,"project":false},"capabilities":{"api":["tasks:read"],"events":[],"ui":[]},"external":{"base_url_setting":"refused_url","health_path":"/healthz","install_path":"/mesh/install","events_path":"/mesh/events"}}', 'catalog', 'custom');
INSERT INTO instance_extension_policy (extension_id, allowed) VALUES ('keep', true), ('keep-rotator', true), ('keep-refused', true);
INSERT INTO instance_extension_settings (extension_id, settings, enabled, source) VALUES
 ('keep', '{"keep_url":"http://127.0.0.1:8097"}', true, 'admin'),
 ('keep-rotator', '{"rotator_url":"http://127.0.0.1:8097"}', true, 'admin'),
 ('keep-refused', '{"refused_url":"http://service.example.test"}', true, 'admin');

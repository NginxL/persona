// PersonaScreenCore (#7927): the wire and the capture decisions Persona Screen uses, a module of their own so changes
// to the rest of this package (the route session, the quality tiers) never change the helper's bytes and its Screen
// Recording grant. Re-exported, so everything that imports PersonaScreenKit sees them as before.
@_exported import PersonaScreenCore

package dev.watchyard.link

/** phone.md P6.1's channels; the app's JS creates the same ids, so either side may go first. */
object Channels {
  const val NEEDS_YOU = "wy_needs_you"
  const val LIMITS = "wy_limits"
  const val MACHINES = "wy_machines"
  const val SWITCH = "wy_switch"
  const val DRAIN = "wy_drain"
  const val USAGE = "wy_usage"
  const val TEST = "wy_test"
  const val CONNECTION = "wy_connection"

  /** id, name, importance (NotificationManager's IMPORTANCE_* values). */
  val ALL = listOf(
    Triple(NEEDS_YOU, "Sessions", 4),
    Triple(LIMITS, "Limits", 3),
    Triple(SWITCH, "Account switch", 2),
    Triple(DRAIN, "Drain", 3),
    Triple(USAGE, "Usage notices", 2),
    Triple(MACHINES, "Machines", 2),
    Triple(TEST, "Test", 3),
    Triple(CONNECTION, "Connection (always on)", 1),
  )

  /** The ids a payload's `channel` may name (everything but the link's own connection channel). */
  val KNOWN = ALL.map { it.first }.filter { it != CONNECTION }.toSet()

  const val GROUP = "wy"
}

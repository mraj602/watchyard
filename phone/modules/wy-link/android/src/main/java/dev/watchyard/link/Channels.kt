package dev.watchyard.link

/** phone.md P6.1's channels; the app's JS creates the same ids, so either side may go first. */
object Channels {
  const val NEEDS_YOU = "wy_needs_you"
  const val LIMITS = "wy_limits"
  const val MACHINES = "wy_machines"
  const val USAGE = "wy_usage"
  const val TEST = "wy_test"
  const val CONNECTION = "wy_connection"

  /** id, name, importance (NotificationManager's IMPORTANCE_* values). */
  val ALL = listOf(
    Triple(NEEDS_YOU, "Needs you", 4),
    Triple(LIMITS, "Limits", 3),
    Triple(MACHINES, "Machines", 2),
    Triple(USAGE, "Usage changes", 2),
    Triple(TEST, "Test", 3),
    Triple(CONNECTION, "Connection (always on)", 1),
  )

  const val GROUP = "wy"
}

import EventKit
import Foundation

/// Manages adding recipes to the user's default calendar via EventKit.
enum CalendarService {

    private static let store = EKEventStore()

    /// Request calendar access. Returns true if granted.
    static func requestAccess() async -> Bool {
        do {
            return try await store.requestFullAccessToEvents()
        } catch {
            print("[Calendar] Access request failed: \(error)")
            return false
        }
    }

    /// Current authorization status.
    static var isAuthorized: Bool {
        EKEventStore.authorizationStatus(for: .event) == .fullAccess
    }

    /// Create a calendar event for a planned meal. Returns the event identifier
    /// on success, or nil if the save failed.
    @discardableResult
    static func addEvent(for meal: PlannedMeal) -> String? {
        guard isAuthorized else { return nil }

        let event = EKEvent(eventStore: store)
        event.title = "🍽 \(meal.recipeName)"
        event.startDate = meal.plannedDate
        event.endDate = meal.plannedDate.addingTimeInterval(Double(meal.timeMinutes) * 60)
        event.notes = meal.calendarNotes
        event.url = URL(string: "dinnerdecider://meal/\(meal.id.uuidString)")
        event.calendar = store.defaultCalendarForNewEvents

        // Reminder 30 min before so they can start prep.
        event.addAlarm(EKAlarm(relativeOffset: -30 * 60))

        do {
            try store.save(event, span: .thisEvent)
            return event.eventIdentifier
        } catch {
            print("[Calendar] Failed to save event: \(error)")
            return nil
        }
    }

    /// Remove a previously created calendar event.
    static func removeEvent(identifier: String) {
        guard isAuthorized,
              let event = store.event(withIdentifier: identifier) else { return }
        try? store.remove(event, span: .thisEvent)
    }
}

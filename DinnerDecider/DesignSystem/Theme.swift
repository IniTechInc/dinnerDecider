import SwiftUI

/// DinnerDecider design tokens.
///
/// All brand colors live in Assets.xcassets as color sets with light and dark
/// appearance variants, tuned to meet WCAG contrast targets (body/small text
/// at least 4.5:1, UI glyphs and large/bold text at least 3:1). Views should
/// reference these tokens instead of ad-hoc system colors so the palette stays
/// coherent and legible in both light and dark mode.
extension Color {

    // MARK: Brand

    /// Hero terracotta. Buttons, key actions, brand moments, primary tint.
    /// Tuned so white text on the fill clears 4.5:1 in both appearances.
    static let brandPrimary = Color("BrandPrimary")

    /// Sage green. Fresh/healthy accents, on-device Gemma badge, success-ish
    /// states, confident confidence scores.
    static let brandSecondary = Color("BrandSecondary")

    /// Golden amber. Highlights, "almost there" / missing items, low-confidence
    /// caution flags, demo-mode badge, staple stars.
    static let brandAccent = Color("BrandAccent")

    // MARK: Surfaces

    /// App background. Warm cream in light, warm charcoal in dark.
    static let surfacePrimary = Color("SurfacePrimary")

    /// Elevated surfaces (cards, rows). Soft off-white in light, lifted
    /// charcoal in dark.
    static let surfaceSecondary = Color("SurfaceSecondary")

    // MARK: Text

    /// Primary reading text. Warm near-black / warm near-white.
    static let textPrimary = Color("TextPrimary")

    /// Secondary text (captions, subtitles). Warm greige, at least 4.5:1.
    static let textSecondary = Color("TextSecondary")

    // MARK: Neutral

    /// Separators, disabled states, tertiary hairlines. Warm greige.
    static let separatorNeutral = Color("SeparatorNeutral")
}

/// Typography tokens.
///
/// Pairing: DM Serif Display for large display text and recipe names, DM Sans
/// for all functional/body UI. Every token scales with Dynamic Type via
/// `relativeTo`. Rule: never use DM Serif Display below the title2 scale (it
/// ships a single weight and reads poorly at small sizes); reach for a DM Sans
/// token instead.
extension Font {

    // MARK: Display (DM Serif Display) - title2 scale and up only

    private static let serif = "DMSerifDisplay-Regular"

    /// Largest brand/display text (onboarding headlines). Scales with .largeTitle.
    static let displayLarge = Font.custom(serif, size: 34, relativeTo: .largeTitle)

    /// Display title for prominent headers. Scales with .title.
    static let displayTitle = Font.custom(serif, size: 28, relativeTo: .title)

    /// Recipe names and card titles. Scales with .title2. Smallest allowed serif.
    static let recipeName = Font.custom(serif, size: 22, relativeTo: .title2)

    // MARK: Functional (DM Sans)

    private static let sans = "DMSans-Regular"
    private static let sansMedium = "DMSans-Medium"
    private static let sansSemibold = "DMSans-SemiBold"
    private static let sansBold = "DMSans-Bold"

    /// Standard body copy. Scales with .body.
    static let dmBody = Font.custom(sans, size: 17, relativeTo: .body)

    /// Callout / secondary body. Scales with .callout.
    static let dmCallout = Font.custom(sans, size: 16, relativeTo: .callout)

    /// Subheadline. Scales with .subheadline.
    static let dmSubheadline = Font.custom(sans, size: 15, relativeTo: .subheadline)

    /// Footnote. Scales with .footnote.
    static let dmFootnote = Font.custom(sans, size: 13, relativeTo: .footnote)

    /// Caption. Scales with .caption.
    static let dmCaption = Font.custom(sans, size: 12, relativeTo: .caption)

    /// Emphasised body / inline headline. DM Sans SemiBold, scales with .headline.
    static let dmHeadline = Font.custom(sansSemibold, size: 17, relativeTo: .headline)

    /// Section headers and list titles. DM Sans SemiBold, scales with .subheadline.
    static let dmSectionHeader = Font.custom(sansSemibold, size: 15, relativeTo: .subheadline)

    /// Strong emphasis / prominent button labels. DM Sans Bold, scales with .body.
    static let dmBodyBold = Font.custom(sansBold, size: 17, relativeTo: .body)

    /// Medium-weight small label (badges). Scales with .caption.
    static let dmCaptionMedium = Font.custom(sansMedium, size: 12, relativeTo: .caption)
}

/// One-time UIKit appearance configuration for surfaces that SwiftUI cannot yet
/// style with custom fonts directly (navigation bar titles).
enum AppTheme {

    /// Applies DM Serif Display to large navigation titles and DM Sans SemiBold
    /// to inline titles, both scaled with Dynamic Type and coloured with the
    /// TextPrimary token. Call once at launch.
    static func configureNavigationBar() {
        let textColor = UIColor(named: "TextPrimary") ?? .label

        let appearance = UINavigationBarAppearance()
        appearance.configureWithTransparentBackground()

        if let serif = UIFont(name: "DMSerifDisplay-Regular", size: 34) {
            let scaled = UIFontMetrics(forTextStyle: .largeTitle).scaledFont(for: serif)
            appearance.largeTitleTextAttributes = [.font: scaled, .foregroundColor: textColor]
        }
        if let sans = UIFont(name: "DMSans-SemiBold", size: 17) {
            let scaled = UIFontMetrics(forTextStyle: .headline).scaledFont(for: sans)
            appearance.titleTextAttributes = [.font: scaled, .foregroundColor: textColor]
        }

        UINavigationBar.appearance().standardAppearance = appearance
        UINavigationBar.appearance().scrollEdgeAppearance = appearance
        UINavigationBar.appearance().compactAppearance = appearance
    }
}

extension View {
    /// Replaces the default grouped/scroll background with the warm surface
    /// token: cream in light mode, charcoal in dark. Keeps list rows readable
    /// while giving the app its branded backdrop.
    func dinnerSurfaceBackground() -> some View {
        scrollContentBackground(.hidden)
            .background(Color.surfacePrimary.ignoresSafeArea())
    }
}

/// Corner radius scale.
enum Radius {
    /// 8pt. Chips, small controls.
    static let small: CGFloat = 8
    /// 12pt. Cards, banners, standard containers.
    static let medium: CGFloat = 12
    /// 16pt. Large cards, thumbnails.
    static let large: CGFloat = 16
}

/// Spacing scale (multiples of 4).
enum Spacing {
    static let xs: CGFloat = 4
    static let sm: CGFloat = 8
    static let md: CGFloat = 12
    static let lg: CGFloat = 16
    static let xl: CGFloat = 24
}

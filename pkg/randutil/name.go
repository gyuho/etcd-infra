// Code in this file is derived from github.com/lucasepe/codename
// Original work Copyright (c) 2021 Luca Sepe
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package randutil

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

var (
	//nolint:gochecknoglobals // Package-level state.
	wordIndex uint64
	//nolint:gochecknoglobals // Package-level state.
	wordIndexOnce sync.Once
)

// Word returns a random one word from the codename pool.
// It increments a global atomic index to ensure that calling it
// either concurrently or in sequence returns different words.
func Word() string {
	wordIndexOnce.Do(func() {
		//nolint:gosec // G404: Weak RNG is acceptable for codenames
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		//nolint:gosec // G115: integer overflow conversion safe
		startIdx := uint64(r.Intn(len(words)))
		atomic.StoreUint64(&wordIndex, startIdx)
	})

	idx := atomic.AddUint64(&wordIndex, 1)

	return words[idx%uint64(len(words))]
}

// words is a combined pool of adjectives and nouns from github.com/lucasepe/codename.
// Hyphenated words have been removed to simplify cluster ID parsing.
//
//nolint:gochecknoglobals,lll // Constant pool, long lines unavoidable.
var words = []string{
	"able", "above", "absolute", "accepted", "accurate", "active", "actual", "adapted", "adapting", "adequate",
	"adjusted", "advanced", "alert", "alive", "allowed", "allowing", "amazed", "amazing", "amusing", "apparent",
	"artistic", "assured", "assuring", "awaited", "awake", "aware", "balanced", "becoming", "beloved", "better",
	"big", "blessed", "bold", "boss", "brave", "brief", "bright", "bursting", "busy", "calm",
	"capable", "capital", "careful", "caring", "casual", "causal", "central", "certain", "champion", "charmed",
	"charming", "cheerful", "chief", "choice", "civil", "classic", "clean", "clear", "clever", "climbing",
	"close", "closing", "coherent", "comic", "communal", "complete", "composed", "concise", "concrete", "content",
	"cool", "correct", "cosmic", "crack", "creative", "credible", "crisp", "crucial", "cuddly", "cunning",
	"curious", "current", "cute", "daring", "darling", "dashing", "dear", "decent", "deciding", "deep",
	"definite", "delicate", "desired", "destined", "devoted", "direct", "discrete", "distinct", "diverse", "divine",
	"dominant", "driven", "driving", "dynamic", "eager", "easy", "electric", "elegant", "emerging", "eminent",
	"enabled", "enabling", "endless", "engaged", "engaging", "enhanced", "enjoyed", "enormous", "enough", "epic",
	"equal", "equipped", "eternal", "ethical", "evident", "evolved", "evolving", "exact", "excited", "exciting",
	"exotic", "expert", "factual", "fair", "faithful", "famous", "fancy", "fast", "feasible", "fine",
	"finer", "firm", "first", "fitting", "fleet", "flexible", "flowing", "fluent", "flying", "fond",
	"frank", "free", "fresh", "full", "fun", "funky", "funny", "game", "generous", "gentle",
	"genuine", "giving", "glad", "glorious", "glowing", "golden", "good", "gorgeous", "grand", "grateful",
	"great", "growing", "grown", "guided", "guiding", "handy", "happy", "hardy", "harmless", "healthy",
	"helped", "helpful", "helping", "heroic", "hip", "holy", "honest", "hopeful", "hot", "huge",
	"humane", "humble", "humorous", "ideal", "immense", "immortal", "immune", "improved", "included", "infinite",
	"informed", "innocent", "inspired", "integral", "intense", "intent", "internal", "intimate", "inviting", "joint",
	"just", "keen", "key", "kind", "knowing", "known", "large", "lasting", "leading", "learning",
	"legal", "legible", "lenient", "liberal", "light", "liked", "literate", "live", "living", "logical",
	"loved", "loving", "loyal", "lucky", "magical", "magnetic", "main", "major", "many", "massive",
	"master", "mature", "maximum", "measured", "meet", "merry", "mighty", "mint", "model", "modern",
	"modest", "moral", "more", "moved", "moving", "musical", "mutual", "national", "native", "natural",
	"nearby", "neat", "needed", "neutral", "next", "nice", "noble", "normal", "notable", "noted",
	"novel", "obliging", "open", "optimal", "optimum", "organic", "oriented", "outgoing", "patient", "peaceful",
	"perfect", "picked", "pleasant", "pleased", "pleasing", "poetic", "polished", "polite", "popular", "positive",
	"possible", "powerful", "precious", "precise", "premium", "prepared", "present", "pretty", "primary", "prime",
	"pro", "probable", "profound", "promoted", "prompt", "proper", "proud", "proven", "pumped", "pure",
	"quality", "quick", "quiet", "rapid", "rare", "rational", "ready", "real", "refined", "regular",
	"related", "relative", "relaxed", "relaxing", "relevant", "relieved", "renewed", "renewing", "resolved", "rested",
	"rich", "right", "robust", "romantic", "ruling", "sacred", "safe", "saved", "saving", "secure",
	"select", "selected", "sensible", "settled", "settling", "sharing", "sharp", "shining", "simple", "sincere",
	"singular", "skilled", "smart", "smashing", "smiling", "smooth", "social", "solid", "sought", "sound",
	"special", "splendid", "square", "stable", "star", "steady", "sterling", "still", "stirred", "stirring",
	"striking", "strong", "stunning", "subtle", "suitable", "suited", "summary", "sunny", "super", "superb",
	"supreme", "sweeping", "sweet", "talented", "teaching", "tender", "thankful", "thorough", "tidy", "tight",
	"together", "tolerant", "top", "topical", "tops", "touched", "touching", "tough", "true", "trusted",
	"trusting", "trusty", "ultimate", "unbiased", "uncommon", "unique", "upward", "usable", "useful", "valid",
	"valued", "vast", "verified", "viable", "vital", "wanted", "warm", "wealthy", "welcome", "welcomed",
	"well", "whole", "willing", "winning", "wired", "wise", "witty", "wondrous", "workable", "working",
	"worthy", "abomination", "abyss", "agent", "amethyst", "amphibian", "andromeda", "annihilus", "anole", "anthem",
	"alchemist", "apocalypse", "aquagirl", "aquaman", "arachne", "arcade", "arcana", "archangel", "arclight", "ares",
	"argent", "arisia", "armadillo", "armor", "armory", "arrowette", "arsenal", "arsenic", "artemis", "artiee",
	"asgardian", "aspen", "atlas", "atom", "atomic", "avalanche", "azazel", "azrael", "aztec", "ballistic",
	"banshee", "barb", "barbarella", "baroness", "barracuda", "bastion", "batgirl", "batman", "battle", "batwoman",
	"bazooka", "beak", "beast", "bebop", "becatron", "bedlam", "beef", "beetle", "bella", "belphegor",
	"bengal", "bette", "binary", "bionic", "bishop", "bizarro", "blackbat", "blackheart", "blackout", "blade",
	"blastaar", "blindfold", "blink", "blitzkrieg", "blizzard", "blob", "blockbuster", "blok", "bloke", "blonde",
	"bloodaxe", "bloodberry", "bloodscream", "bloodstorm", "bloodstrike", "bloom", "blossom", "blue", "bluestreak", "blur",
	"boom", "boomer", "boomerang", "booster", "bounty", "bubbles", "bug", "bulldozer", "bulleteer", "bulletgirl",
	"bullseye", "bumblebee", "burnout", "bushwacker", "buttercup", "butterfly", "cable", "calamity", "calendar", "caliban",
	"callisto", "calypso", "cammi", "cammy", "cannonball", "captain", "cardiac", "caretaker", "carnage", "cat",
	"catseye", "catwoman", "cerebro", "chameleon", "changeling", "chase", "chat", "cherry", "chimera", "chronomancer",
	"circuit", "cleopatra", "cloak", "clobber", "clobberella", "clover", "coagula", "cobra", "cobweb", "colleen",
	"colossus", "colt", "comedian", "comet", "conan", "constrictor", "contessa", "controller", "copperhead", "copycat",
	"cornelius", "corsair", "cosmo", "cottonmouth", "countess", "crane", "crazy", "crossbones", "crystal", "cyber",
	"cybergirl", "cyblade", "cyborg", "cyclone", "cyclops", "cypher", "dagger", "daredevil", "darkhawk", "darkstar",
	"darwin", "dawn", "dawnstar", "dazzler", "dead", "deadpool", "death", "deathbird", "deathcry", "deathlok",
	"deathstrike", "defenders", "demogoblin", "destine", "destiny", "devastator", "diablo", "diamond", "diamondback", "doctor",
	"doll", "dollar", "dolphin", "domino", "donatello", "doomsday", "doorman", "doppelganger", "dormammu", "dove",
	"dracula", "dragonfly", "dragonna", "drax", "dream", "dumb", "dusk", "dust", "dyna", "dynamite",
	"earthquake", "echo", "ego", "electra", "electro", "elektra", "elite", "elixir", "elongated", "empath",
	"empowered", "empress", "enchantress", "energizer", "epoch", "eradicator", "eternals", "eternity", "excalibur", "exodus",
	"expediter", "ezekiel", "fairchild", "faith", "falcon", "fallen", "famine", "fantomah", "fantomette", "fantomex",
	"fathom", "fenris", "feral", "fever", "fire", "firebird", "firebrand", "firedrake", "firefly", "firelord",
	"firestar", "firestorm", "fixer", "flaberella", "flamebird", "flash", "flatman", "flint", "flora", "forearm",
	"forerunner", "forge", "freak", "freefall", "frenzy", "fury", "galactus", "galvatron", "gambit", "gamora",
	"gangbuster", "ganymede", "garganta", "gargoyle", "gargoyles", "gateway", "gauntlet", "genesis", "ghost", "gladiator",
	"glitter", "glory", "goliath", "grandmaster", "graphics", "gravity", "greymalkin", "groot", "guardian", "guardsmen",
	"gunslinger", "gwen", "hairball", "hammerhead", "hardball", "harpoon", "haven", "havok", "hawk", "hawkeye",
	"hawkgirl", "hawkman", "hawkwoman", "heather", "hellboy", "hellcat", "hercules", "hiroim", "hitman", "hobgoblin",
	"hooded", "horridus", "howard", "hulk", "hulkling", "humbug", "huntara", "huntress", "husk", "hussar",
	"hydra", "hyperion", "ice", "iceman", "impulse", "indigo", "inertia", "infragirl", "inhumans", "ink",
	"insect", "invisible", "iron", "jackpot", "jaguar", "jigsaw", "joker", "jolt", "joystick", "jubilee",
	"judomaster", "juggernaut", "jungle", "juniper", "justice", "karate", "karatecha", "karma", "katana", "killmonger",
	"kinetix", "kingpin", "kitty", "klaw", "knockout", "komodo", "kree", "kronos", "lady", "ladyhawk",
	"lanolin", "laurel", "lavagirl", "layla", "leader", "leatherhead", "leatherneck", "legion", "leonardo", "leopardon",
	"lester", "lettuce", "liberty", "lifeguard", "lightning", "lightspeed", "lilandra", "lilith", "lime", "lionheart",
	"little", "lizard", "lockheed", "lockjaw", "longshot", "looker", "luckman", "maddog", "madripoor", "madrox",
	"maestro", "magik", "maginty", "magma", "magneto", "magus", "malice", "mandarin", "mandrill", "mandroid",
	"manhunter", "manitou", "manta", "mantis", "marionette", "marrow", "martian", "mastermind", "mathemanic", "mauler",
	"maximus", "medusa", "megatron", "menace", "mentor", "mephisto", "metamorpho", "meteorite", "michaelangelo", "microbe",
	"microchip", "micromax", "midnight", "mimic", "mindworm", "miracleman", "mirage", "misty", "mockingbird", "mongoose",
	"mongu", "monstress", "moondragon", "moonstar", "moonstone", "morbius", "mysterio", "mystique", "nebula", "negative",
	"nemesis", "neon", "network", "nextwave", "night", "nightcat", "nightcrawler", "nighthawk", "nightmare", "nightshade",
	"nightstar", "nightveil", "nightwing", "nitro", "nocturne", "nomad", "northstar", "nova", "nuke", "odin",
	"ogun", "onslaught", "onyx", "oracle", "orion", "overlord", "owl", "owlman", "owlwoman", "paladin",
	"pandemic", "pantha", "parasite", "patch", "patriot", "payback", "penance", "penguin", "pestilence", "phalanx",
	"phantom", "phoenix", "photon", "piledriver", "plastic", "plasma", "poison", "polaris", "post", "power",
	"princess", "prism", "prodigy", "psylocke", "punisher", "purifiers", "pyro", "quasar", "queen", "quicksilver",
	"rage", "raider", "rainbow", "rainmaker", "rampage", "random", "raphael", "raptor", "rapture", "reaper",
	"redwing", "reptil", "rescue", "revanche", "reverse", "rhino", "ricochet", "rictor", "riddler", "risque",
	"rocket", "rockslide", "rogue", "sailor", "sandman", "saracen", "sasquatch", "satana", "satellite", "saturn",
	"sauron", "savant", "scalphunter", "scarecrow", "scarlet", "scorpion", "scourge", "scrambler", "scream", "screwball",
	"secret", "sentinel", "sentinels", "sentry", "sepulcher", "serpentor", "shadow", "shadowcat", "shadoweyes", "shaman",
	"shamrock", "shocker", "shockwave", "shotgun", "shredder", "shriek", "shrinking", "siege", "silhouette", "silver",
	"silverclaw", "silvermane", "siren", "skullbuster", "skyrocket", "slapstick", "slayback", "sleeper", "sleepwalker", "slipstream",
	"smasher", "snowbird", "songbird", "spartan", "spectrum", "speedball", "speedy", "spellbinder", "sphinx", "spider",
	"spiral", "spirit", "spitfire", "spoiler", "spot", "sprite", "spy", "spyke", "squirrel", "starbolt",
	"stardust", "starfire", "starfox", "stargirl", "starhawk", "starwoman", "steel", "stinger", "stingray", "storm",
	"stormtrooper", "stranger", "stripperella", "stryfe", "stunner", "sunfire", "sunspot", "supergirl", "supergran", "superman",
	"supernaut", "superwoman", "swift", "switch", "swordsman", "synch", "tag", "talisman", "talkback", "talon",
	"talos", "tank", "tara", "tarantula", "tarot", "taskmaster", "tattoo", "tecna", "tempest", "tenebrous",
	"terror", "terry", "thing", "thunder", "thunderball", "thunderbird", "thunderbolt", "tiger", "timeslip", "tinkerer",
	"titaness", "titania", "toad", "tombstone", "toxin", "trauma", "triathlon", "triceraton", "triplicate", "triton",
	"tsunami", "turbo", "tyrannus", "ultimatum", "ultimo", "ultra", "ultragirl", "ultrawoman", "ultron", "unicorn",
	"valkyrie", "vampirella", "vampiro", "vanisher", "vapor", "vector", "velocity", "vengeance", "venom", "venus",
	"vermin", "vigilante", "vindicator", "violations", "violet", "viper", "virtuous", "vision", "vivisector", "vixen",
	"vogue", "void", "voodoo", "vulcan", "vulture", "wallflower", "wallop", "wallow", "warbird", "warbound",
	"warhawk", "warlock", "warpath", "warstar", "wasp", "watchmen", "wendigo", "whirlwind", "whistler", "whizzer",
	"wiccan", "widget", "wild", "wildcat", "winged", "witchblade", "witchfire", "wolfpack", "wolfsbane", "wolverine",
	"wonder", "wraith", "wrecker", "yellowjacket", "zombie",
}

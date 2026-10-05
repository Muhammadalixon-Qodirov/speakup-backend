package utils

import "math/rand"

// Speaking topics - shown to users during sessions.
var topics = []string{
	// Daily Life
	"What does your typical day look like?",
	"Describe your favorite place to relax.",
	"What hobby would you like to start?",
	"Talk about your favorite meal and how to cook it.",
	"What's the best gift you've ever received?",

	// Travel
	"If you could visit any country, where would you go?",
	"Describe your most memorable trip.",
	"What do you always pack when traveling?",
	"City vacation or nature trip - which do you prefer?",
	"Talk about a place in Uzbekistan every tourist should visit.",

	// Technology
	"How has your phone changed your daily life?",
	"What app could you not live without?",
	"Do you think AI will replace human jobs?",
	"Social media: helpful or harmful?",
	"Describe a technology you wish existed.",

	// Education
	"What subject do you wish you studied more?",
	"How do you learn new things most effectively?",
	"Should university education be free?",
	"What's the most useful skill you've learned?",
	"Online learning vs classroom - pros and cons.",

	// Career
	"Describe your dream job.",
	"What makes a good leader?",
	"Would you rather work from home or in an office?",
	"What career advice would you give your younger self?",
	"Talk about a challenge you faced at work or school.",

	// Culture
	"What traditions from your culture are you proud of?",
	"Describe a festival or celebration you enjoy.",
	"How is your generation different from your parents'?",
	"What movie or book changed your perspective?",
	"If you could have dinner with anyone, who would it be?",

	// Debate Topics
	"Is social media making us more or less connected?",
	"Should homework be abolished?",
	"Is it better to save money or enjoy life now?",
	"Are zoos ethical?",
	"Should voting be mandatory?",

	// Fun & Creative
	"If you had a superpower, what would it be?",
	"You win $1 million - what do you do first?",
	"Describe yourself in three words.",
	"What would you do if you were invisible for a day?",
	"Create a new holiday - what would it celebrate?",

	// Business
	"What startup idea would you invest in?",
	"How important is branding for a business?",
	"What makes a great customer experience?",
	"Should companies allow unlimited vacation days?",
	"Talk about an entrepreneur you admire.",
}

// GetRandomTopic returns a random speaking topic.
func GetRandomTopic() string {
	return topics[rand.Intn(len(topics))]
}

// GetAllTopics returns all available topics.
func GetAllTopics() []string {
	return topics
}

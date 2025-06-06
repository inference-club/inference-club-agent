from django.test import TestCase
from django.urls import reverse
from rest_framework.test import APITestCase
from rest_framework import status
from .models import VideoGenService

# Create your tests here.

class VideoGenServiceModelTest(TestCase):
    def setUp(self):
        self.service = VideoGenService.objects.create(
            name="Test Video Service",
            url="https://example.com",
            slug="test-video-service",
            type="HUNYUAN_VIDEO"
        )

    def test_service_creation(self):
        self.assertEqual(self.service.name, "Test Video Service")
        self.assertEqual(self.service.type, "HUNYUAN_VIDEO")
        self.assertEqual(str(self.service), "Test Video Service")

class VideoGenServiceAPITest(APITestCase):
    def setUp(self):
        self.service = VideoGenService.objects.create(
            name="Test Video Service",
            url="https://example.com",
            slug="test-video-service",
            type="HUNYUAN_VIDEO"
        )
        self.url = reverse('videogenservice-list')

    def test_list_services(self):
        response = self.client.get(self.url)
        self.assertEqual(response.status_code, status.HTTP_200_OK)
        self.assertEqual(len(response.data), 1)

    def test_create_service(self):
        data = {
            'name': 'New Video Service',
            'url': 'https://new-example.com',
            'slug': 'new-video-service',
            'type': 'HUNYUAN_VIDEO'
        }
        response = self.client.post(self.url, data)
        self.assertEqual(response.status_code, status.HTTP_201_CREATED)
        self.assertEqual(VideoGenService.objects.count(), 2)
